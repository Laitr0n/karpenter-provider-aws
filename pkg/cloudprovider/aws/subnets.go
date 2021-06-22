/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/awslabs/karpenter/pkg/apis/provisioning/v1alpha1"
	"github.com/patrickmn/go-cache"
	"go.uber.org/zap"
)

type SubnetProvider struct {
	ec2api ec2iface.EC2API
	cache  *cache.Cache
}

func NewSubnetProvider(ec2api ec2iface.EC2API) *SubnetProvider {
	return &SubnetProvider{
		ec2api: ec2api,
		cache:  cache.New(CacheTTL, CacheCleanupInterval),
	}
}

func (s *SubnetProvider) Get(ctx context.Context, provisioner *v1alpha1.Provisioner, constraints *Constraints) ([]*ec2.Subnet, error) {
	// 1. Get all viable subnets for this provisioner
	subnets, err := s.getSubnets(ctx, provisioner)
	if err != nil {
		return nil, err
	}
	// 2. Filter by subnet name if constrained
	if name := constraints.GetSubnetName(); name != nil {
		subnets = filter(byName(aws.StringValue(name)), subnets)
	}
	// 3. Filter by subnet tag key if constrained
	if tagKey := constraints.GetSubnetTagKey(); tagKey != nil {
		subnets = filter(byTagKey(*tagKey), subnets)
	}
	// 4. Filter by zones if constrained
	if len(constraints.Zones) != 0 {
		subnets = filter(byZones(constraints.Zones), subnets)
	}
	return subnets, nil
}

func (s *SubnetProvider) getSubnets(ctx context.Context, provisioner *v1alpha1.Provisioner) ([]*ec2.Subnet, error) {
	if subnets, ok := s.cache.Get(provisioner.Spec.Cluster.Name); ok {
		return subnets.([]*ec2.Subnet), nil
	}
	output, err := s.ec2api.DescribeSubnetsWithContext(ctx, &ec2.DescribeSubnetsInput{Filters: []*ec2.Filter{{
		Name:   aws.String("tag-key"), // Subnets must be tagged for the cluster
		Values: []*string{aws.String(fmt.Sprintf(ClusterTagKeyFormat, provisioner.Spec.Cluster.Name))},
	}}})
	if err != nil {
		return nil, fmt.Errorf("describing subnets, %w", err)
	}
	zap.S().Debugf("Successfully discovered %d subnets for cluster %s", len(output.Subnets), provisioner.Spec.Cluster.Name)
	s.cache.Set(provisioner.Spec.Cluster.Name, output.Subnets, CacheTTL)
	return output.Subnets, nil
}

func filter(predicate func(*ec2.Subnet) bool, subnets []*ec2.Subnet) []*ec2.Subnet {
	result := []*ec2.Subnet{}
	for _, subnet := range subnets {
		if predicate(subnet) {
			result = append(result, subnet)
		}
	}
	return result
}

func byName(name string) func(*ec2.Subnet) bool {
	return func(subnet *ec2.Subnet) bool {
		for _, tag := range subnet.Tags {
			if aws.StringValue(tag.Key) == "Name" {
				return aws.StringValue(tag.Value) == name
			}
		}
		return false
	}
}

func byTagKey(tagKey string) func(*ec2.Subnet) bool {
	return func(subnet *ec2.Subnet) bool {
		for _, tag := range subnet.Tags {
			if aws.StringValue(tag.Key) == tagKey {
				return true
			}
		}
		return false
	}
}

func byZones(zones []string) func(*ec2.Subnet) bool {
	return func(subnet *ec2.Subnet) bool {
		for _, zone := range zones {
			if aws.StringValue(subnet.AvailabilityZone) == zone {
				return true
			}
		}
		return false
	}
}
