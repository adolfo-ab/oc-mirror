package integration_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openshift/oc-mirror/tests/integration/pkg/ocmirror"
)

// OCP-75219 - diskToMirror should not require network access to source registries.
//
// Outbound network access is simulated as blocked by pointing HTTP_PROXY/HTTPS_PROXY at an
// unreachable local address, while excluding localhost (used by the destination registry and
// oc-mirror's own local cache) via NO_PROXY. Any accidental external call would fail fast with a
// connection error, so a successful run proves diskToMirror only relied on the local archive and
// the destination registry.
var _ = Describe("diskToMirror without network access", func() {
	var workDir string

	BeforeEach(func() {
		workDir = setupWorkDir()
	})

	AfterEach(func() {
		cleanupWorkDir(workDir)
	})

	It("should mirror successfully from a local archive when outbound network access is blocked", func() {
		iscPath := filepath.Join(iscDir, "secure_policy", "isc-operators-only.yaml")

		By("running mirrorToDisk with network access")
		result, err := runner.MirrorToDisk(ctx, iscPath, workDir)
		expectOcMirrorCommandSuccess(result, err)

		By("removing the local cache to simulate a clean disk-to-mirror environment")
		Expect(os.RemoveAll(cacheDir)).To(Succeed())

		By("running diskToMirror with outbound network access blocked")
		offlineRunner := ocmirror.NewRunner(os.Getenv("OC_MIRROR_BINARY")).
			WithEnv([]string{
				"HTTP_PROXY=http://127.0.0.1:1",
				"HTTPS_PROXY=http://127.0.0.1:1",
				"NO_PROXY=localhost,127.0.0.1",
			})
		result, err = offlineRunner.DiskToMirror(ctx, iscPath, workDir, testRegistry.Endpoint(),
			"--dest-tls-verify=false")
		expectOcMirrorCommandSuccess(result, err)

		By("verifying images were mirrored to the destination registry")
		expectSuccessfulMirrorInRegistry(iscPath, *testRegistry)
	})
})
