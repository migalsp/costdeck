package scaling

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

const (
	testAccessKey  = "AKIDCOSTDECKTEST"
	testSecretKey  = "costdeck-test-secret"
	providerRegion = "eu-west-1"
)

type awsCall struct {
	Action    string
	AccessKey string
	Region    string
	Service   string
	Form      url.Values
}

type awsReply func(form url.Values) (int, string)

type fakeAWS struct {
	mu      sync.Mutex
	calls   []awsCall
	replies map[string]awsReply
}

var sigV4Scope = regexp.MustCompile(`Credential=([^/]+)/\d{8}/([^/]+)/([^/]+)/aws4_request`)

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	call := awsCall{Action: r.PostForm.Get("Action"), Form: r.PostForm}
	if scope := sigV4Scope.FindStringSubmatch(r.Header.Get("Authorization")); scope != nil {
		call.AccessKey, call.Region, call.Service = scope[1], scope[2], scope[3]
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	reply, ok := f.replies[call.Action]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, queryError("InvalidAction", "unexpected action "+call.Action))
		return
	}
	code, body := reply(r.PostForm)
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func (f *fakeAWS) recorded() []awsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeAWS) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func fakeAWSProvider(t *testing.T, f *fakeAWS) *AWSProvider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &AWSProvider{cfg: aws.Config{
		Region:       providerRegion,
		Credentials:  awscredentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   srv.Client(),
		Retryer:      func() aws.Retryer { return aws.NopRetryer{} },
	}}
}

func answer(code int, body string) awsReply {
	return func(url.Values) (int, string) { return code, body }
}

func rdsResponse(action, result string) string {
	return fmt.Sprintf(`<%[1]sResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><%[1]sResult>%[2]s</%[1]sResult>`+
		`<ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata></%[1]sResponse>`, action, result)
}

func queryError(code, message string) string {
	return fmt.Sprintf(`<ErrorResponse><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error>`+
		`<RequestId>req-1</RequestId></ErrorResponse>`, code, message)
}

func ec2Response(action, body string) string {
	return fmt.Sprintf(`<%[1]sResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>req-1</requestId>%[2]s</%[1]sResponse>`, action, body)
}

func ec2Error(code, message string) string {
	return fmt.Sprintf(`<Response><Errors><Error><Code>%s</Code><Message>%s</Message></Error></Errors><RequestID>req-1</RequestID></Response>`, code, message)
}

func auroraClusterXML(id, engine, clusterStatus string, tags map[string]string) string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var tagXML strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&tagXML, "<Tag><Key>%s</Key><Value>%s</Value></Tag>", k, tags[k])
	}
	return fmt.Sprintf(`<DBCluster><DBClusterIdentifier>%s</DBClusterIdentifier><Engine>%s</Engine><Status>%s</Status><TagList>%s</TagList></DBCluster>`,
		id, engine, clusterStatus, tagXML.String())
}

func ec2InstanceXML(id, state, name string) string {
	tags := ""
	if name != "" {
		tags = "<tagSet><item><key>team</key><value>web</value></item><item><key>Name</key><value>" + name + "</value></item></tagSet>"
	}
	return fmt.Sprintf(`<item><instanceId>%s</instanceId><instanceState><code>0</code><name>%s</name></instanceState>%s</item>`, id, state, tags)
}

func reservationsXML(instances ...string) string {
	return "<reservationSet><item><reservationId>r-1</reservationId><instancesSet>" + strings.Join(instances, "") + "</instancesSet></item></reservationSet>"
}

func ec2Filters(form url.Values) map[string][]string {
	out := map[string][]string{}
	for i := 1; form.Get(fmt.Sprintf("Filter.%d.Name", i)) != ""; i++ {
		var values []string
		for j := 1; form.Get(fmt.Sprintf("Filter.%d.Value.%d", i, j)) != ""; j++ {
			values = append(values, form.Get(fmt.Sprintf("Filter.%d.Value.%d", i, j)))
		}
		out[form.Get(fmt.Sprintf("Filter.%d.Name", i))] = values
	}
	return out
}

func TestAWSAuroraScaleStartsAndStopsTheClusterInItsRegion(t *testing.T) {
	f := &fakeAWS{replies: map[string]awsReply{
		"StartDBCluster": answer(http.StatusOK, rdsResponse("StartDBCluster", auroraClusterXML("orders-db", "aurora-postgresql", "starting", nil))),
		"StopDBCluster":  answer(http.StatusOK, rdsResponse("StopDBCluster", auroraClusterXML("orders-db", "aurora-postgresql", "stopping", nil))),
	}}
	p := fakeAWSProvider(t, f)
	ctx := context.Background()

	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: AWSTypeAurora, Identifier: "orders-db", Region: "us-west-2"}, true); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: AWSTypeAurora, Identifier: "orders-db"}, false); err != nil {
		t.Fatalf("stop: %v", err)
	}

	calls := f.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want one start and one stop", calls)
	}
	want := []struct{ action, region string }{{"StartDBCluster", "us-west-2"}, {"StopDBCluster", providerRegion}}
	for i, c := range calls {
		if c.Action != want[i].action || c.Form.Get("DBClusterIdentifier") != "orders-db" || c.Service != "rds" ||
			c.Region != want[i].region || c.AccessKey != testAccessKey {
			t.Errorf("call %d = %s %s in %s (%s, key %s), want %s orders-db in %s signed for rds",
				i, c.Action, c.Form.Get("DBClusterIdentifier"), c.Region, c.Service, c.AccessKey, want[i].action, want[i].region)
		}
	}
}

func TestAWSAuroraScaleTreatsInvalidClusterStateAsDone(t *testing.T) {
	alreadyThere := answer(http.StatusBadRequest, queryError("InvalidDBClusterStateFault", "DbCluster orders-db is not in available state"))
	f := &fakeAWS{replies: map[string]awsReply{"StartDBCluster": alreadyThere, "StopDBCluster": alreadyThere}}
	p := fakeAWSProvider(t, f)
	target := finopsv1.ExternalTarget{Type: AWSTypeAurora, Identifier: "orders-db"}

	for _, active := range []bool{true, false} {
		if err := p.Scale(context.Background(), target, active); err != nil {
			t.Errorf("Scale(active=%v) on a cluster already moving there = %v, want success", active, err)
		}
	}

	missing := answer(http.StatusNotFound, queryError("DBClusterNotFoundFault", "DBCluster orders-db not found"))
	f.replies = map[string]awsReply{"StartDBCluster": missing, "StopDBCluster": missing}
	for _, active := range []bool{true, false} {
		err := p.Scale(context.Background(), target, active)
		var notFound *rdstypes.DBClusterNotFoundFault
		if !errors.As(err, &notFound) || isInvalidClusterState(err) {
			t.Errorf("Scale(active=%v) on a missing cluster = %v, want the DBClusterNotFoundFault returned", active, err)
		}
	}
}

func TestAWSAuroraReadiness(t *testing.T) {
	cases := []struct {
		name          string
		active        bool
		reply         awsReply
		want          bool
		wantErrSubstr string
	}{
		{"available is up", true, answer(http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters>"+auroraClusterXML("orders-db", "aurora-mysql", "available", nil)+"</DBClusters>")), true, ""},
		{"starting is not up yet", true, answer(http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters>"+auroraClusterXML("orders-db", "aurora-mysql", "starting", nil)+"</DBClusters>")), false, ""},
		{"stopped is down", false, answer(http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters>"+auroraClusterXML("orders-db", "aurora-mysql", "stopped", nil)+"</DBClusters>")), true, ""},
		{"stopping is not down yet", false, answer(http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters>"+auroraClusterXML("orders-db", "aurora-mysql", "stopping", nil)+"</DBClusters>")), false, ""},
		{"available is not down", false, answer(http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters>"+auroraClusterXML("orders-db", "aurora-mysql", "available", nil)+"</DBClusters>")), false, ""},
		{"no cluster in the answer", true, answer(http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters></DBClusters>")), false, "cluster not found: orders-db"},
		{"API error", true, answer(http.StatusNotFound, queryError("DBClusterNotFoundFault", "DBCluster orders-db not found")), false, "DBCluster orders-db not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeAWS{replies: map[string]awsReply{"DescribeDBClusters": c.reply}}
			p := fakeAWSProvider(t, f)
			ready, err := p.IsReady(context.Background(), finopsv1.ExternalTarget{Type: AWSTypeAurora, Identifier: "orders-db", Region: "us-east-2"}, c.active)
			if c.wantErrSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrSubstr) || ready {
					t.Fatalf("IsReady = %v, %v; want false and an error containing %q", ready, err, c.wantErrSubstr)
				}
			} else if err != nil || ready != c.want {
				t.Fatalf("IsReady = %v, %v; want %v", ready, err, c.want)
			}
			calls := f.recorded()
			if len(calls) != 1 || calls[0].Form.Get("DBClusterIdentifier") != "orders-db" || calls[0].Region != "us-east-2" {
				t.Errorf("calls = %+v, want one DescribeDBClusters for orders-db in the target's region", calls)
			}
		})
	}
}

func TestAWSDiscoverAuroraFiltersEngineAndTagsAcrossPages(t *testing.T) {
	f := &fakeAWS{replies: map[string]awsReply{"DescribeDBClusters": func(form url.Values) (int, string) {
		if form.Get("Marker") == "" {
			return http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters>"+
				auroraClusterXML("orders-db", "aurora-postgresql", "available", map[string]string{"env": "dev", "team": "shop"})+
				auroraClusterXML("legacy-pg", "postgres", "available", map[string]string{"env": "dev"})+
				auroraClusterXML("prod-db", "aurora-mysql", "available", map[string]string{"env": "prod"})+
				"</DBClusters><Marker>page-2</Marker>")
		}
		return http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters>"+
			auroraClusterXML("reports-db", "aurora-mysql", "stopped", map[string]string{"env": "dev"})+
			auroraClusterXML("untagged-db", "aurora-postgresql", "available", nil)+
			"</DBClusters>")
	}}}
	p := fakeAWSProvider(t, f)

	got, err := p.Discover(context.Background(), AWSTypeAurora, map[string]string{"env": "dev"})
	if err != nil {
		t.Fatal(err)
	}
	want := []finopsv1.ExternalTarget{
		{Provider: ProviderAWS, Type: AWSTypeAurora, Identifier: "orders-db", Region: providerRegion, Status: "available"},
		{Provider: ProviderAWS, Type: AWSTypeAurora, Identifier: "reports-db", Region: providerRegion, Status: "stopped"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Discover(env=dev) = %+v, want the Aurora clusters tagged env=dev from both pages, without plain postgres", got)
	}
	calls := f.recorded()
	if len(calls) != 2 || calls[0].Form.Get("Marker") != "" || calls[1].Form.Get("Marker") != "page-2" || calls[0].Form.Get("DBClusterIdentifier") != "" {
		t.Errorf("calls = %+v, want an unfiltered first page and a second page from the marker", calls)
	}

	f.reset()
	all, err := p.Discover(context.Background(), AWSTypeAurora, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(all))
	for _, c := range all {
		ids = append(ids, c.Identifier)
	}
	if !slices.Equal(ids, []string{"orders-db", "prod-db", "reports-db", "untagged-db"}) {
		t.Errorf("Discover(no tags) = %v, want every Aurora cluster", ids)
	}
}

func TestAWSDiscoverAuroraReturnsAPIErrors(t *testing.T) {
	f := &fakeAWS{replies: map[string]awsReply{"DescribeDBClusters": answer(http.StatusForbidden, queryError("AccessDenied", "not authorized to perform rds:DescribeDBClusters"))}}
	got, err := fakeAWSProvider(t, f).Discover(context.Background(), AWSTypeAurora, nil)
	if err == nil || !strings.Contains(err.Error(), "not authorized to perform rds:DescribeDBClusters") || got != nil {
		t.Errorf("Discover = %v, %v; want no targets and the access error", got, err)
	}
}

func TestAWSEC2ScaleStartsAndStopsTheInstanceInItsRegion(t *testing.T) {
	f := &fakeAWS{replies: map[string]awsReply{
		"StartInstances": answer(http.StatusOK, ec2Response("StartInstances", "<instancesSet></instancesSet>")),
		"StopInstances":  answer(http.StatusOK, ec2Response("StopInstances", "<instancesSet></instancesSet>")),
	}}
	p := fakeAWSProvider(t, f)
	ctx := context.Background()

	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: AWSTypeEC2, Identifier: "i-0abc", Region: "ap-south-1"}, true); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: AWSTypeEC2, Identifier: "i-0abc"}, false); err != nil {
		t.Fatalf("stop: %v", err)
	}
	calls := f.recorded()
	want := []struct{ action, region string }{{"StartInstances", "ap-south-1"}, {"StopInstances", providerRegion}}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i, c := range calls {
		if c.Action != want[i].action || c.Form.Get("InstanceId.1") != "i-0abc" || c.Form.Get("InstanceId.2") != "" ||
			c.Region != want[i].region || c.Service != "ec2" {
			t.Errorf("call %d = %s %v in %s (%s), want %s of i-0abc only in %s", i, c.Action, c.Form, c.Region, c.Service, want[i].action, want[i].region)
		}
	}
}

func TestAWSEC2ScaleWrapsErrorsWithTheInstance(t *testing.T) {
	refused := answer(http.StatusBadRequest, ec2Error("IncorrectInstanceState", "The instance is not in a state from which it can be started."))
	f := &fakeAWS{replies: map[string]awsReply{"StartInstances": refused, "StopInstances": refused}}
	p := fakeAWSProvider(t, f)
	for _, c := range []struct {
		active bool
		prefix string
	}{{true, "failed to start EC2 instance i-0abc: "}, {false, "failed to stop EC2 instance i-0abc: "}} {
		err := p.Scale(context.Background(), finopsv1.ExternalTarget{Type: AWSTypeEC2, Identifier: "i-0abc"}, c.active)
		if err == nil || !strings.HasPrefix(err.Error(), c.prefix) || !strings.Contains(err.Error(), "IncorrectInstanceState") {
			t.Errorf("Scale(active=%v) = %v, want an error starting with %q naming the AWS code", c.active, err, c.prefix)
		}
	}
}

func TestAWSEC2Readiness(t *testing.T) {
	describe := func(state string) awsReply {
		return answer(http.StatusOK, ec2Response("DescribeInstances", reservationsXML(ec2InstanceXML("i-0abc", state, "web"))))
	}
	cases := []struct {
		name          string
		active        bool
		reply         awsReply
		want          bool
		wantErrSubstr string
	}{
		{"running is up", true, describe("running"), true, ""},
		{"pending is not up yet", true, describe("pending"), false, ""},
		{"stopped is down", false, describe("stopped"), true, ""},
		{"stopping is not down yet", false, describe("stopping"), false, ""},
		{"running is not down", false, describe("running"), false, ""},
		{"no reservation", true, answer(http.StatusOK, ec2Response("DescribeInstances", "<reservationSet></reservationSet>")), false, "EC2 instance not found: i-0abc"},
		{"API error", false, answer(http.StatusBadRequest, ec2Error("InvalidInstanceID.NotFound", "The instance ID 'i-0abc' does not exist")), false, "does not exist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeAWS{replies: map[string]awsReply{"DescribeInstances": c.reply}}
			ready, err := fakeAWSProvider(t, f).IsReady(context.Background(), finopsv1.ExternalTarget{Type: AWSTypeEC2, Identifier: "i-0abc", Region: "us-east-2"}, c.active)
			if c.wantErrSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrSubstr) || ready {
					t.Fatalf("IsReady = %v, %v; want false and an error containing %q", ready, err, c.wantErrSubstr)
				}
			} else if err != nil || ready != c.want {
				t.Fatalf("IsReady = %v, %v; want %v", ready, err, c.want)
			}
			calls := f.recorded()
			if len(calls) != 1 || calls[0].Form.Get("InstanceId.1") != "i-0abc" || calls[0].Region != "us-east-2" {
				t.Errorf("calls = %+v, want one DescribeInstances for i-0abc in the target's region", calls)
			}
		})
	}
}

func TestAWSDiscoverEC2FiltersByStateAndTagsAcrossPages(t *testing.T) {
	f := &fakeAWS{replies: map[string]awsReply{"DescribeInstances": func(form url.Values) (int, string) {
		if form.Get("NextToken") == "" {
			return http.StatusOK, ec2Response("DescribeInstances", reservationsXML(
				ec2InstanceXML("i-001", "running", "web-1"),
				ec2InstanceXML("i-002", "stopped", ""),
			)+"<nextToken>page-2</nextToken>")
		}
		return http.StatusOK, ec2Response("DescribeInstances", reservationsXML(ec2InstanceXML("i-003", "running", "batch")))
	}}}
	p := fakeAWSProvider(t, f)

	got, err := p.Discover(context.Background(), AWSTypeEC2, map[string]string{"env": "dev", "team": "web"})
	if err != nil {
		t.Fatal(err)
	}
	want := []finopsv1.ExternalTarget{
		{Provider: ProviderAWS, Type: AWSTypeEC2, Identifier: "i-001", Region: providerRegion, Status: "running", Name: "web-1"},
		{Provider: ProviderAWS, Type: AWSTypeEC2, Identifier: "i-002", Region: providerRegion, Status: "stopped"},
		{Provider: ProviderAWS, Type: AWSTypeEC2, Identifier: "i-003", Region: providerRegion, Status: "running", Name: "batch"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Discover = %+v, want %+v", got, want)
	}

	calls := f.recorded()
	if len(calls) != 2 || calls[1].Form.Get("NextToken") != "page-2" {
		t.Fatalf("calls = %+v, want two pages, the second from the token", calls)
	}
	for i, c := range calls {
		filters := ec2Filters(c.Form)
		if len(filters) != 3 || !slices.Equal(filters["instance-state-name"], []string{"running", "stopped"}) ||
			!slices.Equal(filters["tag:env"], []string{"dev"}) || !slices.Equal(filters["tag:team"], []string{"web"}) {
			t.Errorf("page %d filters = %v, want running/stopped instances tagged env=dev and team=web", i+1, filters)
		}
	}
}

func TestAWSDiscoverEC2WrapsAPIErrors(t *testing.T) {
	f := &fakeAWS{replies: map[string]awsReply{"DescribeInstances": answer(http.StatusForbidden, ec2Error("UnauthorizedOperation", "You are not authorized to perform this operation."))}}
	got, err := fakeAWSProvider(t, f).Discover(context.Background(), AWSTypeEC2, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "failed to describe EC2 instances: ") || !strings.Contains(err.Error(), "UnauthorizedOperation") || got != nil {
		t.Errorf("Discover = %v, %v; want no targets and a wrapped UnauthorizedOperation", got, err)
	}
}

func TestAWSRejectsUnsupportedTypesWithoutCallingAWS(t *testing.T) {
	f := &fakeAWS{}
	p := fakeAWSProvider(t, f)
	ctx := context.Background()
	target := finopsv1.ExternalTarget{Type: "lambda", Identifier: "fn"}

	if err := p.Scale(ctx, target, true); err == nil || err.Error() != "unsupported AWS resource type: lambda" {
		t.Errorf("Scale = %v", err)
	}
	if ready, err := p.IsReady(ctx, target, true); err == nil || ready || err.Error() != "unsupported AWS resource type: lambda" {
		t.Errorf("IsReady = %v, %v", ready, err)
	}
	if got, err := p.Discover(ctx, "lambda", nil); err == nil || got != nil || err.Error() != "discovery unsupported for type: lambda" {
		t.Errorf("Discover = %v, %v", got, err)
	}
	if calls := f.recorded(); len(calls) != 0 {
		t.Errorf("AWS was called for an unsupported type: %+v", calls)
	}
}

func TestAWSValidateConnectivityCallsGetCallerIdentity(t *testing.T) {
	identity := `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>` +
		`<Arn>arn:aws:iam::123456789012:user/costdeck</Arn><UserId>AIDTEST</UserId><Account>123456789012</Account>` +
		`</GetCallerIdentityResult><ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`
	f := &fakeAWS{replies: map[string]awsReply{"GetCallerIdentity": answer(http.StatusOK, identity)}}
	p := fakeAWSProvider(t, f)

	if err := p.ValidateConnectivity(context.Background()); err != nil {
		t.Fatalf("ValidateConnectivity = %v", err)
	}
	calls := f.recorded()
	if len(calls) != 1 || calls[0].Service != "sts" || calls[0].AccessKey != testAccessKey || calls[0].Form.Get("Version") != "2011-06-15" {
		t.Errorf("calls = %+v, want one STS GetCallerIdentity signed with the provider's key", calls)
	}

	f.replies["GetCallerIdentity"] = answer(http.StatusForbidden, queryError("InvalidClientTokenId", "The security token included in the request is invalid."))
	err := p.ValidateConnectivity(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "AWS connectivity check failed: ") || !strings.Contains(err.Error(), "InvalidClientTokenId") {
		t.Errorf("ValidateConnectivity with a bad key = %v", err)
	}

	if p.Name() != ProviderAWS || !slices.Equal(p.ResourceTypes(), []string{AWSTypeAurora, AWSTypeEC2}) || p.Config().Region != providerRegion {
		t.Errorf("Name/ResourceTypes/Config = %s, %v, %s", p.Name(), p.ResourceTypes(), p.Config().Region)
	}
}

func unsetEnvironment(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			t.Setenv(name, value)
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func isolateAWSEnvironment(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	unsetEnvironment(t,
		"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY",
		"AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY", "AWS_SESSION_TOKEN", "AWS_ENDPOINT_URL", "AWS_ROLE_ARN",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_MAX_ATTEMPTS", "AWS_RETRY_MODE",
	)
}

func retrievedAccessKey(t *testing.T, p *AWSProvider) string {
	t.Helper()
	creds, err := p.Config().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve credentials: %v", err)
	}
	return creds.AccessKeyID
}

func TestNewAWSProviderFromCredentialsDefaultsTheRegion(t *testing.T) {
	isolateAWSEnvironment(t)
	for _, c := range []struct{ region, want string }{{"", "us-east-1"}, {"eu-central-1", "eu-central-1"}} {
		p, err := NewAWSProviderFromCredentials(context.Background(), testAccessKey, testSecretKey, c.region)
		if err != nil {
			t.Fatal(err)
		}
		if p.Config().Region != c.want || retrievedAccessKey(t, p) != testAccessKey {
			t.Errorf("region %q: provider region %s, key %s; want %s with the given key", c.region, p.Config().Region, retrievedAccessKey(t, p), c.want)
		}
	}
}

func TestNewAWSProviderUsesTheDefaultCredentialChain(t *testing.T) {
	isolateAWSEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFROMENVIRONMENT")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "environment-secret")
	t.Setenv("AWS_REGION", "sa-east-1")

	p, err := NewAWSProvider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Config().Region != "sa-east-1" || retrievedAccessKey(t, p) != "AKIDFROMENVIRONMENT" {
		t.Errorf("provider region %s, key %s; want both from the environment", p.Config().Region, retrievedAccessKey(t, p))
	}
}

func TestNewAWSProviderFromSecret(t *testing.T) {
	isolateAWSEnvironment(t)
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	secret := func(name string, data map[string]string) client.Object {
		s := &corev1.Secret{Name: name, Namespace: "costdeck", Data: map[string][]byte{}}
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		return s
	}
	keys := map[string]string{"AWS_ACCESS_KEY_ID": testAccessKey, "AWS_SECRET_ACCESS_KEY": testSecretKey}
	withRegion := map[string]string{"AWS_ACCESS_KEY_ID": testAccessKey, "AWS_SECRET_ACCESS_KEY": testSecretKey, "AWS_REGION": "eu-north-1"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		secret("keys", keys),
		secret("keys-and-region", withRegion),
		secret("no-secret-key", map[string]string{"AWS_ACCESS_KEY_ID": testAccessKey}),
	).Build()

	cases := []struct {
		name, secret, region string
		wantRegion           string
		wantErr              string
	}{
		{"region argument", "keys", "ca-central-1", "ca-central-1", ""},
		{"default region", "keys", "", "us-east-1", ""},
		{"region from the secret when none is given", "keys-and-region", "", "eu-north-1", ""},
		{"a given region beats the secret's", "keys-and-region", "ca-central-1", "ca-central-1", ""},
		{"missing secret", "absent", "", "", "failed to read secret costdeck/absent: "},
		{"incomplete secret", "no-secret-key", "", "", "secret costdeck/no-secret-key is missing AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewAWSProviderFromSecret(context.Background(), c, tc.secret, "costdeck", tc.region)
			if tc.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tc.wantErr) || p != nil {
					t.Fatalf("NewAWSProviderFromSecret = %v, %v; want an error starting with %q", p, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Config().Region != tc.wantRegion || retrievedAccessKey(t, p) != testAccessKey {
				t.Errorf("region %s, key %s; want %s with the secret's key", p.Config().Region, retrievedAccessKey(t, p), tc.wantRegion)
			}
		})
	}
}

func TestAWSConstructorsReportABrokenSDKConfiguration(t *testing.T) {
	isolateAWSEnvironment(t)
	t.Setenv("AWS_MAX_ATTEMPTS", "many")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{Name: "keys", Namespace: "costdeck", Data: map[string][]byte{
		"AWS_ACCESS_KEY_ID": []byte(testAccessKey), "AWS_SECRET_ACCESS_KEY": []byte(testSecretKey),
	}}).Build()
	ctx := context.Background()

	fromChain, errChain := NewAWSProvider(ctx)
	fromKeys, errKeys := NewAWSProviderFromCredentials(ctx, testAccessKey, testSecretKey, "")
	fromSecret, errSecret := NewAWSProviderFromSecret(ctx, c, "keys", "costdeck", "")
	for _, got := range []struct {
		provider *AWSProvider
		err      error
		prefix   string
	}{
		{fromChain, errChain, "unable to load AWS SDK config: "},
		{fromKeys, errKeys, "unable to load AWS SDK config from credentials: "},
		{fromSecret, errSecret, "unable to load AWS SDK config from secret: "},
	} {
		if got.provider != nil || got.err == nil || !strings.HasPrefix(got.err.Error(), got.prefix) || !strings.Contains(got.err.Error(), "AWS_MAX_ATTEMPTS=many") {
			t.Errorf("provider %v, error %v; want no provider and an error starting with %q that names the bad setting", got.provider, got.err, got.prefix)
		}
	}
}

func TestEC2InstanceWithoutAStateIsNeitherRunningNorStopped(t *testing.T) {
	if got := instanceState(ec2types.Instance{}); got != "" {
		t.Errorf("state of an instance the API sent without one = %q, want empty instead of a panic", got)
	}
	running := ec2types.Instance{State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}}
	if got := instanceState(running); got != ec2types.InstanceStateNameRunning {
		t.Errorf("state = %q", got)
	}
}
