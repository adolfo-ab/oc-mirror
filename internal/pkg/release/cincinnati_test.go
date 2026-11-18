package release

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	gcrv1 "github.com/google/go-containerregistry/pkg/v1"
	digest "github.com/opencontainers/go-digest"
	specv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"

	"github.com/openshift/oc-mirror/v2/internal/pkg/api/v2alpha1"
	"github.com/openshift/oc-mirror/v2/internal/pkg/folder"
	clog "github.com/openshift/oc-mirror/v2/internal/pkg/log"
	"github.com/openshift/oc-mirror/v2/internal/pkg/mirror"
)

type mockSignature struct {
	Log clog.PluggableLoggerInterface
}

func TestGetReleaseReferenceImages(t *testing.T) {
	log := clog.New("trace")

	tmpDir := t.TempDir()
	err := folder.CreateFolders(filepath.Join(tmpDir, "hold-release", "cincinnati-graph-data"))
	assert.NoError(t, err)

	global := &mirror.GlobalOptions{SecurePolicy: false}
	global.WorkingDir = tmpDir

	_, sharedOpts := mirror.SharedImageFlags()
	_, deprecatedTLSVerifyOpt := mirror.DeprecatedTLSVerifyFlags()
	_, srcOpts := mirror.ImageSrcFlags(global, sharedOpts, deprecatedTLSVerifyOpt, "src-", "screds")
	_, destOpts := mirror.ImageDestFlags(global, sharedOpts, deprecatedTLSVerifyOpt, "dest-", "dcreds")
	_, retryOpts := mirror.RetryFlags()

	opts := mirror.CopyOptions{
		Global:              global,
		DeprecatedTLSVerify: deprecatedTLSVerifyOpt,
		SrcImage:            srcOpts,
		DestImage:           destOpts,
		RetryOpts:           retryOpts,
		Destination:         "oci:test",
		Dev:                 false,
		Mode:                mirror.MirrorToDisk,
	}

	cfg := v2alpha1.ImageSetConfiguration{
		ImageSetConfigurationSpec: v2alpha1.ImageSetConfigurationSpec{
			Mirror: v2alpha1.Mirror{
				Platform: v2alpha1.Platform{
					Architectures: []string{"amd64"},
					Graph:         true,
					Channels: []v2alpha1.ReleaseChannel{
						{
							Name: "stable-4.0",
							Full: true,
						},
						{
							Name: "stable-4.1",
						},
						{
							Name:       "stable-4.2",
							MinVersion: "4.2.0",
							MaxVersion: "4.2.10",
						},
						{
							Name:         "stable-4.2",
							ShortestPath: true,
						},
					},
				},
			},
		},
	}

	cfgNoChannels := v2alpha1.ImageSetConfiguration{
		ImageSetConfigurationSpec: v2alpha1.ImageSetConfigurationSpec{
			Mirror: v2alpha1.Mirror{
				Platform: v2alpha1.Platform{
					Architectures: []string{"amd64"},
					Graph:         true,
					Channels: []v2alpha1.ReleaseChannel{
						{
							Type: v2alpha1.TypeOKD,
							Name: "stable-4.0",
						},
					},
				},
			},
		},
	}

	cfgReleaseKubeVirt := v2alpha1.ImageSetConfiguration{
		ImageSetConfigurationSpec: v2alpha1.ImageSetConfigurationSpec{
			Mirror: v2alpha1.Mirror{
				Platform: v2alpha1.Platform{
					Release:           "test-release-image:v1",
					KubeVirtContainer: true,
				},
			},
		},
	}

	t.Run("TestGetReleaseReferenceImages should pass", func(t *testing.T) {
		c := &mockClient{}
		signature := &mockSignature{Log: log}
		requestQuery := make(chan string, 1)
		defer close(requestQuery)

		handler := getHandlerMulti(t, requestQuery)

		ts := httptest.NewServer(http.HandlerFunc(handler))
		t.Cleanup(ts.Close)

		endpoint, err := url.Parse(ts.URL)
		if err != nil {
			t.Fatalf("should not fail endpoint parse")
		}
		c.url = endpoint
		sch := NewCincinnati(log, nil, &cfg, opts, c, false, signature)
		res, _ := sch.GetReleaseReferenceImages(context.Background())
		if res == nil {
			t.Fatalf("should return a related images")
		}
	})

	t.Run("TestGetReleaseReferenceImages should pass (no channels)", func(t *testing.T) {
		c := &mockClient{}
		signature := &mockSignature{Log: log}
		requestQuery := make(chan string, 1)
		defer close(requestQuery)

		handler := getHandlerMulti(t, requestQuery)

		ts := httptest.NewServer(http.HandlerFunc(handler))
		t.Cleanup(ts.Close)

		endpoint, err := url.Parse(ts.URL)
		if err != nil {
			t.Fatalf("should not fail endpoint parse")
		}
		c.url = endpoint
		sch := NewCincinnati(log, nil, &cfgNoChannels, opts, c, false, signature)
		res, err := sch.GetReleaseReferenceImages(context.Background())

		log.Debug("result from cincinnati %v", res)
		if res == nil || err != nil {
			t.Fatalf("should return a related images")
		}
	})

	t.Run("TestGetReleaseReferenceImages should fail", func(t *testing.T) {
		c := &mockClient{}
		signature := &mockSignature{Log: log}
		requestQuery := make(chan string, 1)
		defer close(requestQuery)

		handler := getHandlerMulti(t, requestQuery)

		ts := httptest.NewServer(http.HandlerFunc(handler))
		t.Cleanup(ts.Close)

		endpoint, err := url.Parse(ts.URL)
		if err != nil {
			t.Fatalf("should not fail endpoint parse")
		}
		c.url = endpoint
		sch := NewCincinnati(log, nil, &cfg, opts, c, true, signature)
		res, _ := sch.GetReleaseReferenceImages(context.Background())

		log.Debug("result from cincinnati %v", res)
		if res == nil {
			t.Fatalf("should return a related images")
		}
	})

	t.Run("TestGetReleaseReferenceImages should pass (platform.release & kubevirt)", func(t *testing.T) {
		c := &mockClient{}
		signature := &mockSignature{Log: log}
		requestQuery := make(chan string, 1)
		defer close(requestQuery)

		handler := getHandlerMulti(t, requestQuery)

		ts := httptest.NewServer(http.HandlerFunc(handler))
		t.Cleanup(ts.Close)

		endpoint, err := url.Parse(ts.URL)
		if err != nil {
			t.Fatalf("should not fail endpoint parse")
		}
		c.url = endpoint

		mm := NewManifest()

		sch := NewCincinnati(log, mm, &cfgReleaseKubeVirt, opts, c, true, signature)
		res, _ := sch.GetReleaseReferenceImages(context.Background())

		log.Debug("result from cincinnati %v", res)
		if res == nil {
			t.Fatalf("should return a related images")
		}
	})
}

type mockManifest struct{}

func NewManifest() mockManifest {
	return mockManifest{}
}

func (o mockSignature) GenerateReleaseSignatures(ctx context.Context, rd []v2alpha1.CopyImageSchema) ([]v2alpha1.CopyImageSchema, error) {
	o.Log.Info("signature verification (mock)")
	return []v2alpha1.CopyImageSchema{}, nil
}

func (o mockManifest) ImageDigest(ctx context.Context, srcContext *types.SystemContext, img string) (string, error) {
	return "123456546546546546546546546", nil
}

func (o mockManifest) GetOCIImageIndex(dir string) (*specv1.Index, error) {
	return nil, nil
}

func (o mockManifest) GetOCIImageManifest(file string) (*specv1.Manifest, error) {
	return nil, nil
}

func (o mockManifest) GetOCIImageFromIndex(dir string) (gcrv1.Image, error) { //nolint:ireturn // as expected by go-containerregistry
	return nil, nil
}

func (o mockManifest) GetOperatorConfig(file string) (*v2alpha1.OperatorConfigSchema, error) {
	return &v2alpha1.OperatorConfigSchema{}, nil
}

func (o mockManifest) ExtractOCILayers(_ gcrv1.Image, toPath, label string) error {
	return nil
}

func (o mockManifest) GetReleaseSchema(filePath string) ([]v2alpha1.RelatedImage, error) {
	return []v2alpha1.RelatedImage{}, nil
}

func (o mockManifest) ConvertOCIIndexToSingleManifest(dir string, oci *specv1.Index) error {
	return nil
}

func (o mockManifest) ImageManifest(ctx context.Context, sourceCtx *types.SystemContext, imgRef string, instanceDigest *digest.Digest) ([]byte, string, error) {
	return nil, "", nil
}

func (o mockManifest) GetManifestListDigests(ctx context.Context, sourceCtx *types.SystemContext, source string) ([]string, error) {
	return nil, nil
}

// OCPBUGS-136257: release channels of two major versions in the same image set configuration.
func TestCollectChannelImagesCrossMajor(t *testing.T) {
	tests := []struct {
		name        string
		graphs      map[string]string
		channels    []v2alpha1.ReleaseChannel
		expVersions []string
		expRequests []string
		expErr      string
	}{{
		name: "CandidateChannels",
		graphs: map[string]string{
			"candidate-4.22": testGraph(t, []string{"4.22.19", "4.22.20", "4.22.21", "4.22.23"}, [][2]int{{0, 1}, {1, 2}, {2, 3}}),
			"candidate-4.23": testGraph(t, []string{"4.22.21", "4.23.0-ec.1"}, [][2]int{{0, 1}}),
			"candidate-5.0": testGraph(t, []string{"4.22.19", "4.22.20", "4.22.21", "4.22.23", "5.0.0-rc.1", "5.0.0"},
				[][2]int{{0, 1}, {1, 2}, {1, 5}, {2, 5}, {4, 5}}),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "candidate-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.20", MaxVersion: "4.22.21"},
			{Name: "candidate-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		},
		expVersions: []string{"4.22.20", "4.22.21", "5.0.0"},
		expRequests: []string{"candidate-4.22", "candidate-5.0", "candidate-5.0"},
	}, {
		name: "StableChannels",
		graphs: map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.19", "4.22.20"}, [][2]int{{0, 1}}),
			"stable-5.0":  testGraph(t, []string{"4.22.19", "4.22.20", "5.0.0"}, [][2]int{{0, 1}, {1, 2}}),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.20", MaxVersion: "4.22.20"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		},
		expVersions: []string{"4.22.20", "5.0.0"},
		expRequests: []string{"stable-4.22", "stable-5.0", "stable-5.0"},
	}, {
		// The 4.22 channel is searched although it is not configured.
		name: "IntermediateSourceMajorChannel",
		graphs: map[string]string{
			"stable-4.21": testGraph(t, []string{"4.21.1", "4.21.9"}, [][2]int{{0, 1}}),
			"stable-4.22": testGraph(t, []string{"4.21.9", "4.22.2"}, [][2]int{{0, 1}}),
			"stable-5.0":  testGraph(t, []string{"4.22.2", "5.0.0"}, [][2]int{{0, 1}}),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.21", Type: v2alpha1.TypeOCP, MinVersion: "4.21.1", MaxVersion: "4.21.1"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		},
		expVersions: []string{"4.21.1", "4.21.9", "4.22.2", "5.0.0"},
		expRequests: []string{"stable-4.21", "stable-5.0", "stable-5.0", "stable-4.21", "stable-4.22"},
	}, {
		// A missing upgrade path is a warning: only the start and target releases are mirrored.
		name: "NoUpgradePathWarning",
		graphs: map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.20"}, nil),
			"stable-5.0":  testGraph(t, []string{"4.22.20", "5.0.0"}, nil),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.20", MaxVersion: "4.22.20"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		},
		expVersions: []string{"4.22.20", "5.0.0"},
		expRequests: []string{"stable-4.22", "stable-5.0", "stable-5.0", "stable-4.22"},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, requestedChannels := newChannelGraphServer(t, tt.graphs)
			t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
			o := &CincinnatiSchema{Log: clog.New("trace"), CincinnatiParams: CincinnatiParams{Arch: "amd64", GraphDataDir: t.TempDir()}}
			flagReport := false

			images, errs := o.collectChannelImages(context.Background(), tt.channels, &flagReport)
			if tt.expErr == "" {
				assert.Empty(t, errs)
			} else if assert.Len(t, errs, 1) {
				assert.ErrorContains(t, errs[0], tt.expErr)
			}

			expImages := make([]string, 0, len(tt.expVersions))
			for _, v := range tt.expVersions {
				expImages = append(expImages, testUpdate(v).Image)
			}
			// Images of the channels and of the cross channel upgrades are not deduplicated here.
			unique := map[string]bool{}
			sources := make([]string, 0, len(images))
			for _, img := range images {
				if !unique[img.Source] {
					unique[img.Source] = true
					sources = append(sources, img.Source)
				}
			}
			assert.ElementsMatch(t, expImages, sources)
			assert.Equal(t, tt.expRequests, requestedChannels())
		})
	}
}

// warnRecorder records the warning messages logged through it.
type warnRecorder struct {
	clog.PluggableLoggerInterface
	warnings []string
}

func (l *warnRecorder) Warn(msg string, args ...interface{}) {
	l.warnings = append(l.warnings, fmt.Sprintf(msg, args...))
}

func TestWarnEUSChannel(t *testing.T) {
	tests := []struct {
		name            string
		channel         v2alpha1.ReleaseChannel
		alreadyReported bool
		expReported     bool
	}{{
		name:        "EUSTwoMinorVersions",
		channel:     v2alpha1.ReleaseChannel{Name: "eus-4.22", MinVersion: "4.20.0", MaxVersion: "4.22.0"},
		expReported: true,
	}, {
		name:    "EUSOneMinorVersion",
		channel: v2alpha1.ReleaseChannel{Name: "eus-4.22", MinVersion: "4.21.0", MaxVersion: "4.22.5"},
	}, {
		name:    "EUSPatchVersions",
		channel: v2alpha1.ReleaseChannel{Name: "eus-4.22", MinVersion: "4.22.0", MaxVersion: "4.22.9"},
	}, {
		// OCPBUGS-136257: ranges that reach a later major version warn too.
		name:        "EUSAcrossMajorVersions",
		channel:     v2alpha1.ReleaseChannel{Name: "eus-4.22", MinVersion: "4.22.20", MaxVersion: "5.0.0"},
		expReported: true,
	}, {
		name:        "EUSAcrossMajorAndMinorVersions",
		channel:     v2alpha1.ReleaseChannel{Name: "eus-4.22", MinVersion: "4.22.0", MaxVersion: "5.2.0"},
		expReported: true,
	}, {
		name:    "EUSReversedAcrossMajorVersions",
		channel: v2alpha1.ReleaseChannel{Name: "eus-5.0", MinVersion: "5.0.0", MaxVersion: "4.22.0"},
	}, {
		// The minor version distance does not overflow.
		name:    "EUSLargestMinorVersions",
		channel: v2alpha1.ReleaseChannel{Name: "eus-4.22", MinVersion: "4.18446744073709551614.0", MaxVersion: "4.18446744073709551615.0"},
	}, {
		name:    "NonEUSAcrossMajorVersions",
		channel: v2alpha1.ReleaseChannel{Name: "stable-5.0", MinVersion: "4.22.20", MaxVersion: "5.0.0"},
	}, {
		name:    "EUSReversedRange",
		channel: v2alpha1.ReleaseChannel{Name: "eus-4.22", MinVersion: "4.22.0", MaxVersion: "4.20.0"},
	}, {
		name:            "AlreadyReported",
		channel:         v2alpha1.ReleaseChannel{Name: "eus-4.22", MinVersion: "4.20.0", MaxVersion: "4.22.0"},
		alreadyReported: true,
		expReported:     true,
	}, {
		name:    "NoMinVersion",
		channel: v2alpha1.ReleaseChannel{Name: "eus-4.22", MaxVersion: "4.22.0"},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &warnRecorder{PluggableLoggerInterface: clog.New("trace")}
			o := &CincinnatiSchema{Log: log}

			reported := o.warnEUSChannel(tt.channel, tt.alreadyReported)
			assert.Equal(t, tt.expReported, reported)
			if tt.expReported && !tt.alreadyReported {
				assert.Len(t, log.warnings, 1)
			} else {
				assert.Empty(t, log.warnings)
			}
		})
	}
}

// passthroughSignature returns the release images unchanged, as if their signatures were valid.
type passthroughSignature struct{}

func (passthroughSignature) GenerateReleaseSignatures(_ context.Context, images []v2alpha1.CopyImageSchema) ([]v2alpha1.CopyImageSchema, error) {
	return images, nil
}

// imageSources returns the unique sources of the images.
func imageSources(images []v2alpha1.CopyImageSchema) []string {
	seen := map[string]bool{}
	sources := []string{}
	for _, img := range images {
		if !seen[img.Source] {
			seen[img.Source] = true
			sources = append(sources, img.Source)
		}
	}
	return sources
}

// releaseImages returns the release images of the versions.
func releaseImages(versions ...string) []string {
	images := make([]string, 0, len(versions))
	for _, v := range versions {
		images = append(images, testUpdate(v).Image)
	}
	return images
}

// The user's channels: candidate-5.0 publishes the 4.22 to 5.0 edges and stable-5.0 only the
// 5.0 patch upgrades. The candidate-5.0 graph must be used whatever its configuration order, for
// the direct 4.22.1 -> 5.0.0 -> 5.0.1 route and for a route through a 4.22 patch release.
func TestGetReleaseReferenceImagesConfiguredConnectingChannel(t *testing.T) {
	routes := []struct {
		name      string
		candidate string
		expImages []string
	}{
		{"DirectEdges", testGraph(t, []string{"4.22.1", "5.0.0"}, [][2]int{{0, 1}}), releaseImages("4.22.1", "5.0.0", "5.0.1")},
		// 4.22.5 is only on the path published by candidate-5.0, which is configured heads only.
		{"PatchUpgrade", testGraph(t, []string{"4.22.1", "4.22.5", "5.0.0"}, [][2]int{{0, 1}, {1, 2}}), releaseImages("4.22.1", "4.22.5", "5.0.0", "5.0.1")},
	}
	stable422 := v2alpha1.ReleaseChannel{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"}
	candidate50 := v2alpha1.ReleaseChannel{Name: "candidate-5.0", Type: v2alpha1.TypeOCP}
	stable50 := v2alpha1.ReleaseChannel{Name: "stable-5.0", Type: v2alpha1.TypeOCP}

	for _, route := range routes {
		for _, tc := range []struct {
			name     string
			channels []v2alpha1.ReleaseChannel
		}{
			{"CandidateFirst", []v2alpha1.ReleaseChannel{candidate50, stable422, stable50}},
			{"CandidateBetween", []v2alpha1.ReleaseChannel{stable422, candidate50, stable50}},
			{"CandidateLast", []v2alpha1.ReleaseChannel{stable422, stable50, candidate50}},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				graphs := map[string]string{
					"stable-4.22":   testGraph(t, []string{"4.22.1"}, nil),
					"candidate-5.0": route.candidate,
					"stable-5.0":    testGraph(t, []string{"5.0.0", "5.0.1"}, [][2]int{{0, 1}}),
				}
				endpoint, _ := newChannelGraphServer(t, graphs)
				t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
				workingDir := t.TempDir()
				require.NoError(t, os.MkdirAll(filepath.Join(workingDir, releaseImageExtractDir, cincinnatiGraphDataDir), 0o755))
				cfg := &v2alpha1.ImageSetConfiguration{ImageSetConfigurationSpec: v2alpha1.ImageSetConfigurationSpec{
					Mirror: v2alpha1.Mirror{Platform: v2alpha1.Platform{Channels: append([]v2alpha1.ReleaseChannel(nil), tc.channels...)}},
				}}
				opts := mirror.CopyOptions{Global: &mirror.GlobalOptions{WorkingDir: workingDir}, Mode: mirror.MirrorToDisk}
				sch := NewCincinnati(clog.New("error"), nil, cfg, opts, &mockClient{}, false, passthroughSignature{})

				images, err := sch.GetReleaseReferenceImages(context.Background())
				require.NoError(t, err)
				assert.ElementsMatch(t, route.expImages, imageSources(images))
				// The configuration is not used to store the graphs or the resolved versions.
				assert.Equal(t, tc.channels, cfg.Mirror.Platform.Channels)
			})
		}
	}
}

func TestCollectChannelImagesConnectingChannels(t *testing.T) {
	graphs := map[string]string{
		"stable-4.22": testGraph(t, []string{"4.22.1", "4.22.2"}, [][2]int{{0, 1}}),
		"stable-5.0":  testGraph(t, []string{"4.22.2", "5.0.0"}, [][2]int{{0, 1}}),
		"stable-5.1":  testGraph(t, []string{"5.0.0", "5.1.0"}, [][2]int{{0, 1}}),
		"candidate-5.0": testGraph(t, []string{"4.22.1", "4.22.3", "5.0.0-rc.1", "5.0.0", "5.0.1"},
			[][2]int{{0, 2}, {2, 3}, {0, 1}, {1, 3}, {3, 4}}),
		"fast-5.0": testGraph(t, []string{"4.22.1", "5.0.0-rc.1", "5.0.0"}, [][2]int{{0, 1}, {1, 2}}),
	}
	stable422 := v2alpha1.ReleaseChannel{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"}
	tests := []struct {
		name        string
		graphs      map[string]string
		channels    []v2alpha1.ReleaseChannel
		failing     []string
		expVersions []string
		// expRequests are the requested channels: the per channel downloads, in configuration
		// order, then the cross channel upgrade calculation, which fetches each graph once.
		expRequests []string
		expErr      string
	}{{
		// The configured stable-5.0 graph connects stable-4.22 to stable-5.1, whose graph has no
		// 4.22 releases. The stable-5.0 graph is fetched once for the cross channel upgrades.
		name: "ConfiguredTargetMajorChannel",
		channels: []v2alpha1.ReleaseChannel{stable422,
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
			{Name: "stable-5.1", Type: v2alpha1.TypeOCP, MinVersion: "5.1.0", MaxVersion: "5.1.0"}},
		expVersions: []string{"4.22.1", "4.22.2", "5.0.0", "5.1.0"},
		expRequests: []string{"stable-4.22", "stable-5.0", "stable-5.1", "stable-5.1", "stable-5.0", "stable-4.22"},
	}, {
		// The stable-5.0 graph is discovered when it is not configured.
		name: "DiscoveredTargetMajorChannel",
		channels: []v2alpha1.ReleaseChannel{stable422,
			{Name: "stable-5.1", Type: v2alpha1.TypeOCP, MinVersion: "5.1.0", MaxVersion: "5.1.0"}},
		expVersions: []string{"4.22.1", "4.22.2", "5.0.0", "5.1.0"},
		expRequests: []string{"stable-4.22", "stable-5.1", "stable-5.1", "stable-5.0", "stable-4.22"},
	}, {
		// Generally available releases are only upgraded through generally available releases,
		// also in a candidate channel, whose all generally available path is used. The
		// configuration order does not matter.
		name: "CandidateChannelGeneralAvailability",
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-5.1", Type: v2alpha1.TypeOCP, MinVersion: "5.1.0", MaxVersion: "5.1.0"},
			{Name: "candidate-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
			stable422},
		expVersions: []string{"4.22.1", "4.22.3", "5.0.0", "5.1.0"},
		expRequests: []string{"stable-5.1", "candidate-5.0", "stable-4.22", "stable-5.1", "candidate-5.0", "stable-5.0", "stable-4.22"},
	}, {
		// The configured candidate-5.0 graph only connects the generally available releases
		// through a pre-release, and the stable-5.0 graph has no 4.22 release: only the start and
		// target releases are mirrored.
		name: "PreReleaseConnectingPathOnly",
		graphs: map[string]string{
			"stable-4.22":   testGraph(t, []string{"4.22.1"}, nil),
			"candidate-5.0": testGraph(t, []string{"4.22.1", "5.0.0-rc.1", "5.0.0"}, [][2]int{{0, 1}, {1, 2}}),
			"stable-5.0":    testGraph(t, []string{"5.0.0", "5.0.1"}, [][2]int{{0, 1}}),
		},
		channels: []v2alpha1.ReleaseChannel{stable422,
			{Name: "candidate-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.1", MaxVersion: "5.0.1"}},
		expVersions: []string{"4.22.1", "5.0.0", "5.0.1"},
	}, {
		// A configured connecting channel that cannot be fetched is an error for that channel.
		name: "ConfiguredChannelRequestError",
		channels: []v2alpha1.ReleaseChannel{stable422,
			{Name: "candidate-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
			{Name: "stable-5.1", Type: v2alpha1.TypeOCP, MinVersion: "5.1.0", MaxVersion: "5.1.0"}},
		failing:     []string{"candidate-5.0"},
		expVersions: []string{"4.22.1", "5.1.0"},
		expErr: "[GetReleaseReferenceImages] error calculating cross channel upgrades: failed to get upgrade graph: " +
			`APIRequestError: channel "candidate-5.0": GetGraphDataFailed: unexpected HTTP status 500 Internal Server Error`,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caseGraphs := graphs
			if tt.graphs != nil {
				caseGraphs = tt.graphs
			}
			endpoint, requestedChannels := newChannelGraphServer(t, caseGraphs, tt.failing...)
			t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
			o := &CincinnatiSchema{Log: clog.New("error"), CincinnatiParams: CincinnatiParams{Arch: "amd64", GraphDataDir: t.TempDir()}}
			flagReport := false

			images, errs := o.collectChannelImages(context.Background(), tt.channels, &flagReport)
			if tt.expErr == "" {
				require.Empty(t, errs)
			} else {
				require.NotEmpty(t, errs)
				assert.ErrorContains(t, errs[len(errs)-1], tt.expErr)
			}
			assert.ElementsMatch(t, releaseImages(tt.expVersions...), imageSources(images))
			if tt.expRequests != nil {
				assert.Equal(t, tt.expRequests, requestedChannels())
			}
		})
	}
}

// The cross channel upgrades of OCP channels are calculated with the OCP Cincinnati API, even when
// the last configured channel is an OKD channel, and the OKD channel graphs are not used.
func TestGetCrossChannelDownloadsOCPClient(t *testing.T) {
	ocpEndpoint, ocpRequests := newChannelGraphServer(t, map[string]string{
		"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
		"stable-5.0":  testGraph(t, []string{"5.0.0", "5.0.1"}, [][2]int{{0, 1}}),
	})
	okdEndpoint, okdRequests := newChannelGraphServer(t, map[string]string{
		"candidate-5.0": testGraph(t, []string{"4.22.1", "5.0.0"}, [][2]int{{0, 1}}),
	})
	t.Setenv("UPDATE_URL_OVERRIDE", ocpEndpoint.String())
	log := &warnRecorder{PluggableLoggerInterface: clog.New("error")}
	cs := CincinnatiSchema{
		Log:              log,
		Client:           &okdClient{transport: &http.Transport{}, url: *okdEndpoint},
		CincinnatiParams: CincinnatiParams{Arch: "amd64", GraphDataDir: t.TempDir()},
	}
	channels := []v2alpha1.ReleaseChannel{
		{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
		{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.1", MaxVersion: "5.0.1"},
		{Name: "candidate-5.0", Type: v2alpha1.TypeOKD, MinVersion: "4.22.1", MaxVersion: "5.0.0"},
	}

	images, err := getCrossChannelDownloads(context.Background(), cs, channels)
	require.NoError(t, err)
	assert.ElementsMatch(t, releaseImages("4.22.1", "5.0.1"), imageSources(images))
	require.Len(t, log.warnings, 1)
	assert.Contains(t, log.warnings[0], `searched the graphs of channels "stable-4.22", "stable-5.0": the graphs of channels "stable-5.0" contain no 4.x releases`)
	assert.Empty(t, okdRequests())
	assert.Equal(t, []string{"stable-5.0", "stable-4.22"}, ocpRequests())
}

// newArchChannelGraphServer mocks a Cincinnati API serving the graphs by "<arch>/<channel>". It
// returns the server URL and a function listing the requested "<arch>/<channel>" in order.
func newArchChannelGraphServer(t *testing.T, graphs map[string]string) (*url.URL, func() []string) {
	t.Helper()
	emptyGraph := testGraph(t, nil, nil)
	var mu sync.Mutex
	var requested []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("arch") + "/" + r.URL.Query().Get("channel")
		mu.Lock()
		requested = append(requested, key)
		mu.Unlock()
		body, ok := graphs[key]
		if !ok {
			body = emptyGraph
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(ts.Close)
	endpoint, err := url.Parse(ts.URL)
	require.NoError(t, err)
	return endpoint, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requested...)
	}
}

// Each architecture uses the graphs of its own channels.
func TestGetReleaseReferenceImagesConnectingChannelArchitectures(t *testing.T) {
	endpoint, requested := newArchChannelGraphServer(t, map[string]string{
		"amd64/stable-4.22":   testGraph(t, []string{"4.22.1"}, nil),
		"amd64/candidate-5.0": testGraph(t, []string{"4.22.1", "4.22.5", "5.0.0"}, [][2]int{{0, 1}, {1, 2}}),
		"amd64/stable-5.0":    testGraph(t, []string{"5.0.0", "5.0.1"}, [][2]int{{0, 1}}),
		"arm64/stable-4.22":   testGraph(t, []string{"4.22.1"}, nil),
		"arm64/candidate-5.0": testGraph(t, []string{"4.22.1", "4.22.7", "5.0.0"}, [][2]int{{0, 1}, {1, 2}}),
		"arm64/stable-5.0":    testGraph(t, []string{"5.0.0", "5.0.1"}, [][2]int{{0, 1}}),
	})
	t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
	workingDir := t.TempDir()
	graphDataDir := filepath.Join(workingDir, releaseImageExtractDir, cincinnatiGraphDataDir)
	require.NoError(t, os.MkdirAll(graphDataDir, 0o755))
	cfg := &v2alpha1.ImageSetConfiguration{ImageSetConfigurationSpec: v2alpha1.ImageSetConfigurationSpec{
		Mirror: v2alpha1.Mirror{Platform: v2alpha1.Platform{
			//nolint:staticcheck // SA1019: the deprecated Architectures field is the per architecture collection under test.
			Architectures: []string{"amd64", "arm64"},
			Channels: []v2alpha1.ReleaseChannel{
				{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
				{Name: "candidate-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
				{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.1", MaxVersion: "5.0.1"},
			},
		}},
	}}
	opts := mirror.CopyOptions{Global: &mirror.GlobalOptions{WorkingDir: workingDir}, Mode: mirror.MirrorToDisk}
	sch := NewCincinnati(clog.New("error"), nil, cfg, opts, &mockClient{}, false, passthroughSignature{})

	images, err := sch.GetReleaseReferenceImages(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, releaseImages("4.22.1", "4.22.5", "4.22.7", "5.0.0", "5.0.1"), imageSources(images))
	for _, arch := range []string{"amd64", "arm64"} {
		for _, ch := range []string{"stable-4.22", "candidate-5.0", "stable-5.0"} {
			assert.FileExists(t, filepath.Join(graphDataDir, arch+"-"+ch+".json"))
			assert.Contains(t, requested(), arch+"/"+ch)
		}
	}
}

// A disk to mirror run repeats the calculation from the stored graphs of the configured and
// discovered connecting channels without reaching the Cincinnati API. It skips the missing stored
// graph of a guessed channel, and fails for a configured channel whose stored graph is missing.
func TestGetCrossChannelDownloadsConnectingChannelsDiskToMirror(t *testing.T) {
	endpoint, requestedChannels := newChannelGraphServer(t, map[string]string{
		"stable-4.22":   testGraph(t, []string{"4.22.1"}, nil),
		"candidate-5.0": testGraph(t, []string{"4.22.1", "4.22.5", "5.0.0"}, [][2]int{{0, 1}, {1, 2}}),
		"stable-5.0":    testGraph(t, []string{"5.0.0", "5.0.2"}, [][2]int{{0, 1}}),
		"stable-5.1":    testGraph(t, []string{"5.0.2", "5.1.0"}, [][2]int{{0, 1}}),
	})
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachableURL := unreachable.URL
	unreachable.Close()

	channels := []v2alpha1.ReleaseChannel{
		{Name: "stable-5.1", Type: v2alpha1.TypeOCP, MinVersion: "5.1.0", MaxVersion: "5.1.0"},
		{Name: "candidate-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
	}
	graphDataDir := t.TempDir()
	cs := CincinnatiSchema{Log: clog.New("error"), CincinnatiParams: CincinnatiParams{Arch: "amd64", GraphDataDir: graphDataDir}}
	cs.Opts.Mode = mirror.MirrorToDisk
	t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
	m2d, err := getCrossChannelDownloads(context.Background(), cs, channels)
	require.NoError(t, err)
	assert.ElementsMatch(t, releaseImages("4.22.1", "4.22.5", "5.0.0", "5.0.2", "5.1.0"), imageSources(m2d))
	requests := len(requestedChannels())

	files, err := os.ReadDir(graphDataDir)
	require.NoError(t, err)
	stored := make([]string, 0, len(files))
	for _, f := range files {
		stored = append(stored, f.Name())
	}
	assert.ElementsMatch(t, []string{"amd64-stable-5.1.json", "amd64-candidate-5.0.json", "amd64-stable-5.0.json", "amd64-stable-4.22.json"}, stored)

	d2m := cs
	d2m.Opts.Mode = mirror.DiskToMirror
	t.Setenv("UPDATE_URL_OVERRIDE", unreachableURL)
	replayed, err := getCrossChannelDownloads(context.Background(), d2m, channels)
	require.NoError(t, err)
	assert.ElementsMatch(t, imageSources(m2d), imageSources(replayed))
	assert.Len(t, requestedChannels(), requests, "disk to mirror must not query the Cincinnati API")

	// The missing graph of the guessed stable-5.0 channel is skipped: no path, only the endpoints.
	require.NoError(t, os.Remove(filepath.Join(graphDataDir, "amd64-stable-5.0.json")))
	endpoints, err := getCrossChannelDownloads(context.Background(), d2m, channels)
	require.NoError(t, err)
	assert.ElementsMatch(t, releaseImages("4.22.1", "5.1.0"), imageSources(endpoints))

	require.NoError(t, os.Remove(filepath.Join(graphDataDir, "amd64-candidate-5.0.json")))
	_, err = getCrossChannelDownloads(context.Background(), d2m, channels)
	require.EqualError(t, err, `failed to get upgrade graph: APIRequestError: channel "candidate-5.0": NoGraphData: No graph data found on disk`)
}

func TestGetCrossChannelDownloadsNoUpgradePathWarning(t *testing.T) {
	tests := []struct {
		name        string
		graphs      map[string]string
		channels    []v2alpha1.ReleaseChannel
		expImages   []string
		expWarning  []string
		expRequests []string
	}{{
		name: "DisconnectedGraphs",
		graphs: map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
			"stable-5.0":  testGraph(t, []string{"4.22.1", "5.0.0"}, nil),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		},
		expImages: releaseImages("4.22.1", "5.0.0"),
		expWarning: []string{`no qualifying unconditional upgrade path from version 4.22.1 in channel "stable-4.22" to version 5.0.0 in channel "stable-5.0"`,
			`searched the graphs of channels "stable-4.22", "stable-5.0"`, "they publish no unconditional update path from 4.22.1 to 5.0.0 through generally available releases"},
		expRequests: []string{"stable-5.0", "stable-4.22"},
	}, {
		// The start release is in none of the searched graphs: only the target release is known.
		name: "StartInNoGraph",
		graphs: map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
			"stable-5.0":  testGraph(t, []string{"4.22.1", "5.0.0"}, [][2]int{{0, 1}}),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.9", MaxVersion: "4.22.9"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		},
		expImages:   releaseImages("5.0.0"),
		expWarning:  []string{"no qualifying unconditional upgrade path from version 4.22.9", "version 4.22.9 is not in any of them"},
		expRequests: []string{"stable-5.0", "stable-4.22"},
	}, {
		name: "TargetWithoutSourceMajorReleases",
		graphs: map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
			"stable-5.0":  testGraph(t, []string{"5.0.0", "5.0.1"}, [][2]int{{0, 1}}),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.1", MaxVersion: "5.0.1"},
		},
		expImages:  releaseImages("4.22.1", "5.0.1"),
		expWarning: []string{`the graphs of channels "stable-5.0" contain no 4.x releases`},
	}, {
		name: "ConditionalEdgeOnly",
		graphs: map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
			"stable-5.0":  conditionalEdgesOnlyGraph,
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		},
		expImages: releaseImages("4.22.1", "5.0.0"),
		expWarning: []string{"no qualifying unconditional upgrade path from version 4.22.1", "conditional update edges are not followed",
			"they publish no unconditional update path from 4.22.1 to 5.0.0"},
	}, {
		// The only route goes through a pre-release, which generally available releases avoid.
		name: "PreReleaseRouteOnly",
		graphs: map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
			"stable-5.0":  testGraph(t, []string{"4.22.1", "5.0.0-rc.1", "5.0.0"}, [][2]int{{0, 1}, {1, 2}}),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		},
		expImages:  releaseImages("4.22.1", "5.0.0"),
		expWarning: []string{"no unconditional update path from 4.22.1 to 5.0.0 through generally available releases"},
	}, {
		// No published path from 4.20.10 reaches the 4.22 releases that fast-5.0 upgrades from.
		name: "Stable4.20ToFast5.0",
		graphs: map[string]string{
			"stable-4.20": testGraph(t, []string{"4.20.10"}, nil),
			"stable-4.21": testGraph(t, []string{"4.20.12", "4.21.30"}, [][2]int{{0, 1}}),
			"stable-4.22": testGraph(t, []string{"4.21.30", "4.22.5"}, [][2]int{{0, 1}}),
			"fast-5.0":    testGraph(t, []string{"4.22.5", "5.0.0", "5.0.1"}, [][2]int{{0, 1}, {1, 2}}),
		},
		channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.20", Type: v2alpha1.TypeOCP, MinVersion: "4.20.10", MaxVersion: "4.20.10"},
			{Name: "fast-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.1", MaxVersion: "5.0.1"},
		},
		expImages:   releaseImages("4.20.10", "5.0.1"),
		expWarning:  []string{"no qualifying unconditional upgrade path from version 4.20.10", `"stable-4.20", "stable-4.21", "stable-4.22", "fast-5.0", "stable-5.0"`},
		expRequests: []string{"fast-5.0", "stable-5.0", "stable-4.20", "stable-4.21", "stable-4.22"},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, requestedChannels := newChannelGraphServer(t, tt.graphs)
			t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
			log := &warnRecorder{PluggableLoggerInterface: clog.New("error")}
			cs := CincinnatiSchema{Log: log, CincinnatiParams: CincinnatiParams{Arch: "amd64", GraphDataDir: t.TempDir()}}

			images, err := getCrossChannelDownloads(context.Background(), cs, tt.channels)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.expImages, imageSources(images))
			require.Len(t, log.warnings, 1)
			assert.Contains(t, log.warnings[0], "NoUpgradePath")
			for _, fact := range tt.expWarning {
				assert.Contains(t, log.warnings[0], fact)
			}
			if tt.expRequests != nil {
				assert.Equal(t, tt.expRequests, requestedChannels())
			}
		})
	}
}

// The mirror run continues with the known releases when there is no cross major version path.
func TestGetReleaseReferenceImagesNoUpgradePathContinues(t *testing.T) {
	endpoint, _ := newChannelGraphServer(t, map[string]string{
		"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
		"stable-5.0":  testGraph(t, []string{"4.22.1", "5.0.0"}, nil),
	})
	t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
	workingDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, releaseImageExtractDir, cincinnatiGraphDataDir), 0o755))
	cfg := &v2alpha1.ImageSetConfiguration{ImageSetConfigurationSpec: v2alpha1.ImageSetConfigurationSpec{
		Mirror: v2alpha1.Mirror{Platform: v2alpha1.Platform{Channels: []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
			{Name: "stable-5.0", Type: v2alpha1.TypeOCP},
		}}},
	}}
	opts := mirror.CopyOptions{Global: &mirror.GlobalOptions{WorkingDir: workingDir}, Mode: mirror.MirrorToDisk}
	log := &warnRecorder{PluggableLoggerInterface: clog.New("error")}
	sch := NewCincinnati(log, nil, cfg, opts, &mockClient{}, false, passthroughSignature{})

	images, err := sch.GetReleaseReferenceImages(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, releaseImages("4.22.1", "5.0.0"), imageSources(images))
	require.Len(t, log.warnings, 1)
	assert.Contains(t, log.warnings[0], "NoUpgradePath: no qualifying unconditional upgrade path from version 4.22.1")
}

// Disk to mirror runs repeat the calculation from the graphs that mirror to disk runs stored: only
// the target graph when it has the path, and only the available graphs of guessed channels.
func TestGetCrossChannelDownloadsWarningDiskToMirror(t *testing.T) {
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachableURL := unreachable.URL
	unreachable.Close()

	run := func(t *testing.T, graphs map[string]string, channels []v2alpha1.ReleaseChannel, failing ...string) (CincinnatiSchema, []v2alpha1.CopyImageSchema, []string) {
		t.Helper()
		endpoint, _ := newChannelGraphServer(t, graphs, failing...)
		t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
		cs := CincinnatiSchema{Log: clog.New("error"), CincinnatiParams: CincinnatiParams{Arch: "amd64", GraphDataDir: t.TempDir()}}
		cs.Opts.Mode = mirror.MirrorToDisk
		m2d, err := getCrossChannelDownloads(context.Background(), cs, channels)
		require.NoError(t, err)
		files, err := os.ReadDir(cs.CincinnatiParams.GraphDataDir)
		require.NoError(t, err)
		stored := make([]string, 0, len(files))
		for _, f := range files {
			stored = append(stored, f.Name())
		}
		d2m := cs
		d2m.Opts.Mode = mirror.DiskToMirror
		t.Setenv("UPDATE_URL_OVERRIDE", unreachableURL)
		replayed, err := getCrossChannelDownloads(context.Background(), d2m, channels)
		require.NoError(t, err)
		assert.ElementsMatch(t, imageSources(m2d), imageSources(replayed))
		return d2m, m2d, stored
	}

	t.Run("TargetChannelPath", func(t *testing.T) {
		_, images, stored := run(t, map[string]string{
			"candidate-4.22": testGraph(t, []string{"4.22.20"}, nil),
			"candidate-5.0":  testGraph(t, []string{"4.22.20", "5.0.0"}, [][2]int{{0, 1}}),
		}, []v2alpha1.ReleaseChannel{
			{Name: "candidate-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.20", MaxVersion: "4.22.20"},
			{Name: "candidate-5.0", Type: v2alpha1.TypeOCP, MinVersion: "5.0.0", MaxVersion: "5.0.0"},
		})
		assert.ElementsMatch(t, releaseImages("4.22.20", "5.0.0"), imageSources(images))
		assert.ElementsMatch(t, []string{"amd64-candidate-5.0.json"}, stored)
	})

	t.Run("StaleGraphOfUnavailableGuessedChannel", func(t *testing.T) {
		endpoint, _ := newChannelGraphServer(t, map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
			"stable-5.1":  testGraph(t, []string{"5.0.0", "5.1.0"}, [][2]int{{0, 1}}),
		}, "stable-5.0")
		t.Setenv("UPDATE_URL_OVERRIDE", endpoint.String())
		cs := CincinnatiSchema{Log: clog.New("error"), CincinnatiParams: CincinnatiParams{Arch: "amd64", GraphDataDir: t.TempDir()}}
		cs.Opts.Mode = mirror.MirrorToDisk
		// A graph of stable-5.0 stored by an earlier run, when its graph was available.
		stale := filepath.Join(cs.CincinnatiParams.GraphDataDir, "amd64-stable-5.0.json")
		require.NoError(t, os.WriteFile(stale, []byte(testGraph(t, []string{"4.22.1", "5.0.0"}, [][2]int{{0, 1}})), 0o600))
		channels := []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
			{Name: "stable-5.1", Type: v2alpha1.TypeOCP, MinVersion: "5.1.0", MaxVersion: "5.1.0"},
		}
		m2d, err := getCrossChannelDownloads(context.Background(), cs, channels)
		require.NoError(t, err)
		assert.ElementsMatch(t, releaseImages("4.22.1", "5.1.0"), imageSources(m2d))
		data, err := os.ReadFile(stale)
		require.NoError(t, err)
		assert.JSONEq(t, `{"nodes":[],"edges":[]}`, string(data))

		d2m := cs
		d2m.Opts.Mode = mirror.DiskToMirror
		t.Setenv("UPDATE_URL_OVERRIDE", unreachableURL)
		replayed, err := getCrossChannelDownloads(context.Background(), d2m, channels)
		require.NoError(t, err)
		assert.ElementsMatch(t, imageSources(m2d), imageSources(replayed))
	})

	t.Run("GuessedChannelUnavailable", func(t *testing.T) {
		channels := []v2alpha1.ReleaseChannel{
			{Name: "stable-4.22", Type: v2alpha1.TypeOCP, MinVersion: "4.22.1", MaxVersion: "4.22.1"},
			{Name: "stable-5.1", Type: v2alpha1.TypeOCP, MinVersion: "5.1.0", MaxVersion: "5.1.0"},
		}
		d2m, images, stored := run(t, map[string]string{
			"stable-4.22": testGraph(t, []string{"4.22.1"}, nil),
			"stable-5.1":  testGraph(t, []string{"5.0.0", "5.1.0"}, [][2]int{{0, 1}}),
		}, channels, "stable-5.0")
		assert.ElementsMatch(t, releaseImages("4.22.1", "5.1.0"), imageSources(images))
		// An empty graph is stored for the unavailable stable-5.0 channel.
		assert.ElementsMatch(t, []string{"amd64-stable-5.1.json", "amd64-stable-5.0.json", "amd64-stable-4.22.json"}, stored)

		// The graph of a required channel must be stored.
		require.NoError(t, os.Remove(filepath.Join(d2m.CincinnatiParams.GraphDataDir, "amd64-stable-4.22.json")))
		_, err := getCrossChannelDownloads(context.Background(), d2m, channels)
		require.EqualError(t, err, `failed to get upgrade graph: APIRequestError: channel "stable-4.22": NoGraphData: No graph data found on disk`)
	})
}
