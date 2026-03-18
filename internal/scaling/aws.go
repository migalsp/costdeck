package scaling

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type AWSProvider struct {
	cfg aws.Config
}

func NewAWSProvider(ctx context.Context) (*AWSProvider, error) {
	// Custom HTTP client with a short timeout to handle VPC endpoint reachability issues quickly
	customHTTPClient := &http.Client{
		Timeout: 3 * time.Second,
	}

	// The SDK will automatically use IAM roles for Service Accounts (IRSA) if available,
	// or fallback to default credential chain.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithHTTPClient(customHTTPClient))
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS SDK config: %w", err)
	}

	return &AWSProvider{cfg: cfg}, nil
}

func (p *AWSProvider) Name() string {
	return "aws"
}

func (p *AWSProvider) Scale(ctx context.Context, target finopsv1.ExternalTarget, active bool) error {
	l := log.FromContext(ctx).WithValues("provider", p.Name(), "target", target.Identifier, "active", active)

	if target.Type != "aurora" {
		return fmt.Errorf("unsupported AWS resource type: %s", target.Type)
	}

	// Use specific region if provided, otherwise default to config region
	clientCfg := p.cfg
	if target.Region != "" {
		clientCfg.Region = target.Region
	}
	rdsClient := rds.NewFromConfig(clientCfg)

	// Aurora clusters
	if active {
		l.Info("Starting AWS Aurora cluster")
		_, err := rdsClient.StartDBCluster(ctx, &rds.StartDBClusterInput{
			DBClusterIdentifier: aws.String(target.Identifier),
		})
		if err != nil {
			// Ignore if it's already started
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
			// Ignore if it's already stopped
			if strings.Contains(err.Error(), "InvalidDBClusterStateFault") {
				l.Info("Cluster is already stopping or stopped")
				return nil
			}
			return err
		}
	}

	return nil
}

func (p *AWSProvider) IsReady(ctx context.Context, target finopsv1.ExternalTarget, active bool) (bool, error) {
	if target.Type != "aurora" {
		return false, fmt.Errorf("unsupported AWS resource type: %s", target.Type)
	}

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
	} else {
		return status == "stopped", nil
	}
}

func (p *AWSProvider) Discover(ctx context.Context, resourceType string) ([]finopsv1.ExternalTarget, error) {
	if resourceType != "aurora" {
		return nil, fmt.Errorf("discovery unsupported for type: %s", resourceType)
	}

	rdsClient := rds.NewFromConfig(p.cfg)
	var targets []finopsv1.ExternalTarget

	// Use paginator to fetch all clusters
	paginator := rds.NewDescribeDBClustersPaginator(rdsClient, &rds.DescribeDBClustersInput{})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}

		for _, cluster := range page.DBClusters {
			// Filter for only Aurora, exclude Serverless v1 since it auto-pauses
			// and standard RDS instances.
			engine := aws.ToString(cluster.Engine)
			if strings.Contains(engine, "aurora") {
				targets = append(targets, finopsv1.ExternalTarget{
					Provider:   "aws",
					Type:       "aurora",
					Identifier: aws.ToString(cluster.DBClusterIdentifier),
					Region:     p.cfg.Region,
					Status:     aws.ToString(cluster.Status),
				})
			}
		}
	}

	return targets, nil
}
