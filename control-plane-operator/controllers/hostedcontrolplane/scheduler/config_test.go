package scheduler

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/hypershift/support/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	componentbasev1 "k8s.io/component-base/config/v1alpha1"
	schedulerv1beta3 "k8s.io/kube-scheduler/config/v1beta3"
	"k8s.io/utils/pointer"
)

func TestGenerateConfig(t *testing.T) {
	g := NewWithT(t)
	leaseDuration, err := time.ParseDuration(config.RecommendedLeaseDuration)
	g.Expect(err).ShouldNot(HaveOccurred())
	renewDeadline, err := time.ParseDuration(config.RecommendedRenewDeadline)
	g.Expect(err).ShouldNot(HaveOccurred())
	retryPeriod, err := time.ParseDuration(config.RecommendedRetryPeriod)
	g.Expect(err).ShouldNot(HaveOccurred())

	leaderElectionConfig := componentbasev1.LeaderElectionConfiguration{
		LeaderElect:   pointer.Bool(true),
		LeaseDuration: metav1.Duration{Duration: leaseDuration},
		RenewDeadline: metav1.Duration{Duration: renewDeadline},
		RetryPeriod:   metav1.Duration{Duration: retryPeriod},
	}

	testCases := []struct {
		name                   string
		profile                configv1.SchedulerProfile
		expectedLeaderElection componentbasev1.LeaderElectionConfiguration
		expectedProfiles       []schedulerv1beta3.KubeSchedulerProfile
	}{
		{
			name:                   "Leader elect args get set correctly, default profile",
			profile:                configv1.LowNodeUtilization,
			expectedLeaderElection: leaderElectionConfig,
			expectedProfiles:       []schedulerv1beta3.KubeSchedulerProfile{},
		},
		{
			name:                   "high node utilization profile",
			profile:                configv1.HighNodeUtilization,
			expectedLeaderElection: leaderElectionConfig,
			expectedProfiles:       []schedulerv1beta3.KubeSchedulerProfile{highNodeUtilizationProfile()},
		},
		{
			name:                   "no scoring profile",
			profile:                configv1.NoScoring,
			expectedLeaderElection: leaderElectionConfig,
			expectedProfiles:       []schedulerv1beta3.KubeSchedulerProfile{highNodeUtilizationProfile()},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := generateConfig(tc.profile)
			if err != nil {
				t.Errorf("unexpected error generated in config")
			}
			var data schedulerv1beta3.KubeSchedulerConfiguration
			err = json.Unmarshal([]byte(config), &data)
			if err != nil {
				t.Errorf("unexpected error parsing config")
			}

			if !reflect.DeepEqual(data.LeaderElection, tc.expectedLeaderElection) {
				t.Errorf("expected leader election parameters not found")
			}
		})
	}
}
