package scaling

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

type AWSProvider struct {
	cfg aws.Config
}

// NewAWSProvider creates an AWS provider using the default credential chain (env vars, IRSA, etc.).
func NewAWSProvider(ctx context.Context) (*AWSProvider, error) {
	customHTTPClient := &http.Client{
		Timeout: 3 * time.Second,
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithHTTPClient(customHTTPClient))
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS SDK config: %w", err)
	}

	return &AWSProvider{cfg: cfg}, nil
}

// NewAWSProviderFromCredentials creates an AWS provider using static credentials.
func NewAWSProviderFromCredentials(ctx context.Context, accessKey, secretKey, region string) (*AWSProvider, error) {
	if region == "" {
		region = "us-east-1"
	}

	customHTTPClient := &http.Client{
		Timeout: 5 * time.Second,
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithHTTPClient(customHTTPClient),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS SDK config from credentials: %w", err)
	}

	return &AWSProvider{cfg: cfg}, nil
}

// NewAWSProviderFromSecret creates an AWS provider using credentials from a K8s Secret.
// The secret must contain keys: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY.
// Optionally AWS_REGION can be specified in the secret.
func NewAWSProviderFromSecret(ctx context.Context, k8sClient client.Client, secretName, namespace, region string) (*AWSProvider, error) {
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
		return nil, fmt.Errorf("failed to read secret %s/%s: %w", namespace, secretName, err)
	}

	accessKey := string(secret.Data["AWS_ACCESS_KEY_ID"])
	secretKey := string(secret.Data["AWS_SECRET_ACCESS_KEY"])

	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("secret %s/%s is missing AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY", namespace, secretName)
	}

	// Use region from secret if available, otherwise use provided region
	if secretRegion := string(secret.Data["AWS_REGION"]); secretRegion != "" && region == "" {
		region = secretRegion
	}
	if region == "" {
		region = "us-east-1"
	}

	customHTTPClient := &http.Client{
		Timeout: 5 * time.Second,
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithHTTPClient(customHTTPClient),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS SDK config from secret: %w", err)
	}

	return &AWSProvider{cfg: cfg}, nil
}

// ValidateConnectivity tests the AWS credentials by calling STS GetCallerIdentity.
func (p *AWSProvider) ValidateConnectivity(ctx context.Context) error {
	stsClient := sts.NewFromConfig(p.cfg)
	_, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("AWS connectivity check failed: %w", err)
	}
	return nil
}

func (p *AWSProvider) Name() string {
	return "aws"
}

func (p *AWSProvider) Scale(ctx context.Context, target finopsv1.ExternalTarget, active bool) error {
	switch target.Type {
	case "aurora":
		return p.scaleAurora(ctx, target, active)
	case "ec2":
		return p.scaleEC2(ctx, target, active)
	default:
		return fmt.Errorf("unsupported AWS resource type: %s", target.Type)
	}
}

func (p *AWSProvider) IsReady(ctx context.Context, target finopsv1.ExternalTarget, active bool) (bool, error) {
	switch target.Type {
	case "aurora":
		return p.isAuroraReady(ctx, target, active)
	case "ec2":
		return p.isEC2Ready(ctx, target, active)
	default:
		return false, fmt.Errorf("unsupported AWS resource type: %s", target.Type)
	}
}

// Discover returns a list of scalable targets, optionally filtered by tags.
func (p *AWSProvider) Discover(ctx context.Context, resourceType string, tags map[string]string) ([]finopsv1.ExternalTarget, error) {
	switch resourceType {
	case "aurora":
		return p.discoverAurora(ctx, tags)
	case "ec2":
		return p.discoverEC2(ctx, tags)
	default:
		return nil, fmt.Errorf("discovery unsupported for type: %s", resourceType)
	}
}

// ─── Aurora ──────────────────────────────────────────────────────────────────

func (p *AWSProvider) scaleAurora(ctx context.Context, target finopsv1.ExternalTarget, active bool) error {
	l := log.FromContext(ctx).WithValues("provider", p.Name(), "target", target.Identifier, "active", active)

	clientCfg := p.cfg
	if target.Region != "" {
		clientCfg.Region = target.Region
	}
	rdsClient := rds.NewFromConfig(clientCfg)

	if active {
		l.Info("Starting AWS Aurora cluster")
		_, err := rdsClient.StartDBCluster(ctx, &rds.StartDBClusterInput{
			DBClusterIdentifier: aws.String(target.Identifier),
		})
		if err != nil {
			if strings.Contains(err.Error(), "InvalidDBClusterStateFault") {
				l.Info("Cluster is already starting or running")
				return nil
			}
			return err
		}
	} else {
		l.Info("Stopping AWS Aurora cluster")
		_, err := rdsClient.StopDBCluster(ctx, &rds.StopDBClusterInput{
			DBClusterIdentifier: aws.String(target.Identifier),
		})
		if err != nil {
			if strings.Contains(err.Error(), "InvalidDBClusterStateFault") {
				l.Info("Cluster is already stopping or stopped")
				return nil
			}
			return err
		}
	}
	return nil
}

func (p *AWSProvider) isAuroraReady(ctx context.Context, target finopsv1.ExternalTarget, active bool) (bool, error) {
	clientCfg := p.cfg
	if target.Region != "" {
		clientCfg.Region = target.Region
	}
	rdsClient := rds.NewFromConfig(clientCfg)

	out, err := rdsClient.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(target.Identifier),
	})
	if err != nil {
		return false, err
	}
	if len(out.DBClusters) == 0 {
		return false, fmt.Errorf("cluster not found: %s", target.Identifier)
	}

	status := aws.ToString(out.DBClusters[0].Status)
	if active {
		return status == "available", nil
	}
	return status == "stopped", nil
}

func (p *AWSProvider) discoverAurora(ctx context.Context, tags map[string]string) ([]finopsv1.ExternalTarget, error) {
	rdsClient := rds.NewFromConfig(p.cfg)
	var targets []finopsv1.ExternalTarget

	paginator := rds.NewDescribeDBClustersPaginator(rdsClient, &rds.DescribeDBClustersInput{})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}

		for _, cluster := range page.DBClusters {
			engine := aws.ToString(cluster.Engine)
			if !strings.Contains(engine, "aurora") {
				continue
			}

			// Apply tag filter if tags are specified
			if len(tags) > 0 && !p.matchRDSTags(cluster.TagList, tags) {
				continue
			}

			targets = append(targets, finopsv1.ExternalTarget{
				Provider:   "aws",
				Type:       "aurora",
				Identifier: aws.ToString(cluster.DBClusterIdentifier),
				Region:     p.cfg.Region,
				Status:     aws.ToString(cluster.Status),
			})
		}
	}

	return targets, nil
}

// matchRDSTags checks if the RDS resource has all specified tags.
func (p *AWSProvider) matchRDSTags(resourceTags []rdstypes.Tag, filterTags map[string]string) bool {
	tagMap := make(map[string]string, len(resourceTags))
	for _, t := range resourceTags {
		tagMap[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	for k, v := range filterTags {
		if tagMap[k] != v {
			return false
		}
	}
	return true
}

// ─── EC2 ─────────────────────────────────────────────────────────────────────

func (p *AWSProvider) scaleEC2(ctx context.Context, target finopsv1.ExternalTarget, active bool) error {
	l := log.FromContext(ctx).WithValues("provider", p.Name(), "target", target.Identifier, "type", "ec2", "active", active)

	clientCfg := p.cfg
	if target.Region != "" {
		clientCfg.Region = target.Region
	}
	ec2Client := ec2.NewFromConfig(clientCfg)

	if active {
		l.Info("Starting EC2 instance")
		_, err := ec2Client.StartInstances(ctx, &ec2.StartInstancesInput{
			InstanceIds: []string{target.Identifier},
		})
		if err != nil {
			return fmt.Errorf("failed to start EC2 instance %s: %w", target.Identifier, err)
		}
	} else {
		l.Info("Stopping EC2 instance")
		_, err := ec2Client.StopInstances(ctx, &ec2.StopInstancesInput{
			InstanceIds: []string{target.Identifier},
		})
		if err != nil {
			return fmt.Errorf("failed to stop EC2 instance %s: %w", target.Identifier, err)
		}
	}
	return nil
}

func (p *AWSProvider) isEC2Ready(ctx context.Context, target finopsv1.ExternalTarget, active bool) (bool, error) {
	clientCfg := p.cfg
	if target.Region != "" {
		clientCfg.Region = target.Region
	}
	ec2Client := ec2.NewFromConfig(clientCfg)

	out, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{target.Identifier},
	})
	if err != nil {
		return false, err
	}

	if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
		return false, fmt.Errorf("EC2 instance not found: %s", target.Identifier)
	}

	state := out.Reservations[0].Instances[0].State.Name
	if active {
		return state == ec2types.InstanceStateNameRunning, nil
	}
	return state == ec2types.InstanceStateNameStopped, nil
}

func (p *AWSProvider) discoverEC2(ctx context.Context, tags map[string]string) ([]finopsv1.ExternalTarget, error) {
	ec2Client := ec2.NewFromConfig(p.cfg)
	var targets []finopsv1.ExternalTarget

	// Build filters: only running + stopped instances (exclude terminated)
	filters := []ec2types.Filter{
		{
			Name:   aws.String("instance-state-name"),
			Values: []string{"running", "stopped"},
		},
	}

	// Add tag filters
	for k, v := range tags {
		filters = append(filters, ec2types.Filter{
			Name:   aws.String("tag:" + k),
			Values: []string{v},
		})
	}

	paginator := ec2.NewDescribeInstancesPaginator(ec2Client, &ec2.DescribeInstancesInput{
		Filters: filters,
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to describe EC2 instances: %w", err)
		}

		for _, reservation := range page.Reservations {
			for _, instance := range reservation.Instances {
				name := p.getEC2Name(instance.Tags)
				id := aws.ToString(instance.InstanceId)

				targets = append(targets, finopsv1.ExternalTarget{
					Provider:   "aws",
					Type:       "ec2",
					Identifier: id,
					Region:     p.cfg.Region,
					Status:     string(instance.State.Name),
					Name:       name,
				})
			}
		}
	}

	return targets, nil
}

// getEC2Name extracts the "Name" tag from EC2 instance tags.
func (p *AWSProvider) getEC2Name(tags []ec2types.Tag) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == "Name" {
			return aws.ToString(t.Value)
		}
	}
	return ""
}
