package supply_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"code.cloudfoundry.org/buildpackapplifecycle/buildpackrunner/resources"
	"github.com/SAP/cloud-authorization-buildpack/pkg/supply"
	"github.com/SAP/cloud-authorization-buildpack/pkg/supply/testdata"
	"github.com/cloudfoundry/libbuildpack"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/open-policy-agent/opa/config"
	"github.com/open-policy-agent/opa/plugins/bundle"
	"github.com/open-policy-agent/opa/plugins/rest"
	"github.com/otiai10/copy"
	"gopkg.in/yaml.v2"
)

//go:generate mockgen -source=supply.go --destination=mocks_test.go --package=supply_test

var _ = Describe("Supply", func() {
	var (
		err          error
		buildDir     string
		depsDir      string
		depsIdx      string
		depDir       string
		supplier     *supply.Supplier
		logger       *libbuildpack.Logger
		mockCtrl     *gomock.Controller
		mockManifest *MockManifest
		buffer       *bytes.Buffer
		vcapServices string
	)

	BeforeEach(func() {
		depsDir, err = os.MkdirTemp("", "test")
		Expect(err).To(BeNil())
		buildDir, err = os.MkdirTemp("", "buildDir")
		Expect(err).To(BeNil())
		Expect(copy.Copy(path.Join("testdata", "policies"), path.Join(buildDir, "policies"))).To(Succeed())

		depsIdx = "42"
		depDir = filepath.Join(depsDir, depsIdx)

		err = os.MkdirAll(depDir, 0755)
		Expect(err).To(BeNil())

		buffer = new(bytes.Buffer)
		logger = libbuildpack.NewLogger(buffer)

		mockCtrl = gomock.NewController(GinkgoT())
		mockManifest = NewMockManifest(mockCtrl)
	})

	JustBeforeEach(func() {
		Expect(os.Setenv("VCAP_SERVICES", vcapServices)).To(Succeed())
		wd, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		buildpackDir := path.Join(filepath.Dir(filepath.Dir(wd)))

		args := []string{buildDir, "", depsDir, depsIdx}
		bps := libbuildpack.NewStager(args, logger, &libbuildpack.Manifest{})

		supplier = &supply.Supplier{
			Stager:       bps,
			Manifest:     mockManifest,
			Log:          logger,
			BuildpackDir: buildpackDir,
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()

		err = os.RemoveAll(depsDir)
		Expect(err).To(BeNil())
		Expect(os.Unsetenv("VCAP_APPLICATION")).To(Succeed())
		Expect(os.Unsetenv("AMS_DATA")).To(Succeed())
	})
	When("VCAP_SERVICES contains a 'authorization' service", func() {
		BeforeEach(func() {
			vcapServices = testdata.EnvWithAuthorization
			os.Setenv("AMS_DATA", `{
              "root": "policies",
              "directories": ["myPolicies0", "myPolicies1"],
            }`)
		})
		It("creates a valid launch.yml", func() {
			Expect(supplier.Run()).To(Succeed())
			launchConfig, err := os.Open(filepath.Join(depDir, "launch.yml"))
			Expect(err).NotTo(HaveOccurred())
			var ld resources.LaunchData
			err = yaml.NewDecoder(launchConfig).Decode(&ld)
			Expect(err).NotTo(HaveOccurred())

			By("specifying proper options", func() {
				Expect(ld.Processes).To(HaveLen(1))
				Expect(ld.Processes[0].Type).To(Equal("opa"))
				Expect(ld.Processes[0].Platforms.Cloudfoundry.SidecarFor).To(Equal([]string{"web"}))
				Expect(ld.Processes[0].Command).To(Equal(path.Join(depDir, "start_opa.sh")))
				Expect(ld.Processes[0].Limits.Memory).To(Equal(100))
				Expect(buffer.String()).To(ContainSubstring("writing launch.yml"))
			})
		})
		It("creates the correct opa config", func() {
			Expect(supplier.Run()).To(Succeed())
			Expect(buffer.String()).To(ContainSubstring("writing opa config"))

			rawConfig, err := os.ReadFile(filepath.Join(depDir, "opa_config.yml"))
			Expect(err).NotTo(HaveOccurred())
			cfg, err := config.ParseConfig(rawConfig, "testId")
			Expect(err).NotTo(HaveOccurred())

			var serviceKey string
			By("specifying the correct bundle options", func() {
				var bundleConfig map[string]bundle.Source
				err = json.Unmarshal(cfg.Bundles, &bundleConfig)
				Expect(err).NotTo(HaveOccurred())
				Expect(bundleConfig).To(HaveKey("SAP"))
				serviceKey = bundleConfig["SAP"].Service
				Expect(serviceKey).NotTo(BeEmpty())
				Expect(bundleConfig["SAP"].Resource).To(Equal("SAP.tar.gz"))
				Expect(*bundleConfig["SAP"].Polling.MinDelaySeconds).To(Equal(int64(10)))
				Expect(*bundleConfig["SAP"].Polling.MaxDelaySeconds).To(Equal(int64(20)))
			})
			By("specifying proper s3 rest config", func() {
				var restConfig map[string]rest.Config
				err = json.Unmarshal(cfg.Services, &restConfig)
				Expect(err).NotTo(HaveOccurred())
				Expect(restConfig).To(HaveKey(serviceKey))
				Expect(restConfig[serviceKey].Credentials.S3Signing).NotTo(BeNil())
				Expect(restConfig[serviceKey].URL).To(Equal("https://s3-eu-central-1.amazonaws.com/my-bucket"))
			})
		})
		It("creates the correct env vars", func() {
			Expect(supplier.Run()).To(Succeed())
			env, err := os.ReadFile(path.Join(buildDir, ".profile.d", "0000_opa_env.sh"))
			Expect(err).NotTo(HaveOccurred())
			expectIsExecutable(path.Join(buildDir, ".profile.d", "0000_opa_env.sh"))
			Expect(env).To(ContainSubstring(fmt.Sprint(`export OPA_URL=`, "http://localhost:9888")))
			Expect(env).To(ContainSubstring(fmt.Sprintf("export AWS_ACCESS_KEY_ID=myawstestaccesskeyid")))
			//Expect(env).To(ContainSubstring(fmt.Sprint(`export opa_binary=`, path.Join(depDir,"opa"))))
			//Expect(env).To(ContainSubstring(fmt.Sprint(`export opa_config=`, path.Join(depDir,"opa_config.yml"))))

		})
		It("provides the OPA executable", func() {
			Expect(supplier.Run()).To(Succeed())
			expectIsExecutable(filepath.Join(depDir, "opa"))
		})
		It("provides the OPA start script", func() {
			Expect(supplier.Run()).To(Succeed())
			expectIsExecutable(filepath.Join(depDir, "start_opa.sh"))
		})
		When("AMS_DATA is not set", func() {
			BeforeEach(func() {
				Expect(os.Unsetenv("AMS_DATA")).To(Succeed())
			})
			It("creates a warning", func() {
				Expect(supplier.Run()).To(Succeed())
				Expect(buffer.String()).To(ContainSubstring("upload no authorization data"))
			})
		})
	})
	When("VCAP_SERVICES is empty", func() {
		JustBeforeEach(func() {
			os.Unsetenv("VCAP_SERVICES")
		})
		It("should abort with err", func() {
			Expect(supplier.Run().Error()).To(ContainSubstring("could not unmarshal VCAP_SERVICES"))
		})
	})

})

func expectIsExecutable(fp string) {
	fi, err := os.Stat(fp)
	Expect(err).NotTo(HaveOccurred())
	//Check if executable by all
	Expect(fi.Mode().Perm() & 0111).To(Equal(fs.FileMode(0111)))
}
