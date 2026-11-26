package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/distribution/reference"
	"github.com/google/go-containerregistry/pkg/authn"
	kauth "github.com/google/go-containerregistry/pkg/authn/kubernetes"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	operatorv1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

const scheduleImageCheckTimeout = time.Minute

type scheduledImagePolicy struct {
	repository name.Repository
	semver     *semverImagePolicy
	currentTag string
}

func parseScheduledImagePolicy(schedule *operatorv1alpha1.NodeOpUpgradeSchedule) (*scheduledImagePolicy, error) {
	if schedule.Spec.ImagePolicy == nil {
		return nil, nil
	}
	policy, err := newSemverImagePolicy(schedule.Spec.ImagePolicy.Semver)
	if err != nil {
		return nil, err
	}
	repositoryName, err := reference.WithName(schedule.Spec.ImagePolicy.Repository)
	if err != nil {
		return nil, fmt.Errorf("invalid imagePolicy.repository: %w", err)
	}
	repository, err := name.NewRepository(repositoryName.Name())
	if err != nil {
		return nil, fmt.Errorf("invalid imagePolicy.repository: %w", err)
	}
	currentImage := schedule.Status.LastSuccessfulImage
	if currentImage == "" {
		currentImage = schedule.Spec.UpgradeTemplate.Image
	}
	// Validate the whole reference before extracting its tag, including a digest
	// when present. Digest-only references cannot establish a semantic version.
	current, err := reference.Parse(currentImage)
	if err != nil {
		return nil, fmt.Errorf("invalid current image: %w", err)
	}
	tagged, ok := current.(reference.Tagged)
	if !ok {
		return nil, fmt.Errorf("current image must include a semantic version tag")
	}
	if _, err := parseSemverTag(tagged.Tag()); err != nil {
		return nil, fmt.Errorf("current image must include a semantic version tag: %w", err)
	}
	return &scheduledImagePolicy{repository: repository, semver: policy, currentTag: tagged.Tag()}, nil
}

func (r *NodeOpUpgradeScheduleReconciler) checkScheduledImage(ctx context.Context,
	schedule *operatorv1alpha1.NodeOpUpgradeSchedule, policy *scheduledImagePolicy) error {
	ctx, cancel := context.WithTimeout(ctx, scheduleImageCheckTimeout)
	defer cancel()
	secrets := append([]corev1.LocalObjectReference{}, schedule.Spec.ImagePolicy.ImagePullSecrets...)
	secrets = append(secrets, schedule.Spec.UpgradeTemplate.ImagePullSecrets...)
	keychain, err := r.scheduleRegistryKeychain(ctx, schedule.Namespace, secrets)
	if err != nil {
		return err
	}
	opts := []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(keychain)}
	if r.registryTransport != nil {
		opts = append(opts, remote.WithTransport(r.registryTransport))
	}
	// List follows registry pagination; only the selected manifest is fetched.
	tags, err := remote.List(policy.repository, opts...)
	if err != nil {
		return fmt.Errorf("list image tags: %w", err)
	}
	tag, err := policy.semver.selectUpgrade(tags, policy.currentTag)
	if err != nil {
		return err
	}
	var availableImage string
	if tag != "" {
		image := policy.repository.Tag(tag)
		descriptor, err := remote.Get(image, opts...)
		if err != nil {
			return fmt.Errorf("resolve selected image digest: %w", err)
		}
		// Preserve the tag for future version comparisons while pinning its content.
		availableImage = image.Name() + "@" + descriptor.Digest.String()
	}
	now := metav1.NewTime(r.currentTime())
	schedule.Status.AvailableImage = availableImage
	schedule.Status.LastCheckTime = &now
	condition := metav1.Condition{
		Type: conditionUpgradeAvailable, Status: metav1.ConditionFalse,
		Reason: "NoNewVersion", Message: "No newer tag matches imagePolicy.semver",
		ObservedGeneration: schedule.Generation, LastTransitionTime: now,
	}
	if availableImage != "" {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "NewVersionAvailable"
		condition.Message = "A newer matching image is available: " + availableImage
	}
	meta.SetStatusCondition(&schedule.Status.Conditions, condition)
	return nil
}

// scheduleRegistryKeychain reads only the referenced secrets in the schedule's
// namespace. The registry library handles credential decoding and matching.
func (r *NodeOpUpgradeScheduleReconciler) scheduleRegistryKeychain(ctx context.Context, namespace string,
	refs []corev1.LocalObjectReference) (authn.Keychain, error) {
	secrets := make([]corev1.Secret, 0, len(refs))
	for _, ref := range refs {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, secret); err != nil {
			return nil, fmt.Errorf("read image pull secret %q: %w", ref.Name, err)
		}
		secrets = append(secrets, *secret)
	}

	// This keychain does not read the operator's local or cloud credentials.
	keychain, err := kauth.NewFromPullSecrets(ctx, secrets)
	if err != nil {
		// Decoder errors may contain secret material, so keep them out of status.
		return nil, fmt.Errorf("image pull secrets contain invalid registry credentials")
	}
	return keychain, nil
}
