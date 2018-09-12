package volman_test

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/jeffpak/local-node-plugin/node"
	"github.com/nu7hatch/gouuid"

	"code.cloudfoundry.org/bbs/models"
	"code.cloudfoundry.org/clock"
	loggingclient "code.cloudfoundry.org/diego-logging-client"
	"code.cloudfoundry.org/durationjson"
	"code.cloudfoundry.org/executor"
	executorinit "code.cloudfoundry.org/executor/initializer"
	"code.cloudfoundry.org/executor/initializer/configuration"
	"code.cloudfoundry.org/inigo/world"
	"code.cloudfoundry.org/lager"
	"code.cloudfoundry.org/lager/lagertest"
	"code.cloudfoundry.org/voldriver"
	"code.cloudfoundry.org/voldriver/driverhttp"
	"github.com/cloudfoundry-incubator/cf-test-helpers/generator"
	. "github.com/onsi/ginkgo"
	ginkgoconfig "github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/ginkgomon"
	"github.com/tedsuo/ifrit/grouper"
)

// these tests could eventually be folded into ../executor/executor_garden_test.go
var _ = Describe("Executor/Garden/Volman", func() {
	var (
		executorClient executor.Client
		process        ifrit.Process
		runner         ifrit.Runner
		cachePath      string
		config         executorinit.ExecutorConfig
		logger         lager.Logger
		env            voldriver.Env
		err            error
	)

	getContainer := func(guid string) executor.Container {
		container, err := executorClient.GetContainer(logger, guid)
		Expect(err).NotTo(HaveOccurred())

		return container
	}

	containerStatePoller := func(guid string) func() executor.State {
		return func() executor.State {
			return getContainer(guid).State
		}
	}
	BeforeEach(func() {
		logger = lagertest.NewTestLogger("volman-executor-tests")
		ctx := context.TODO()
		env = driverhttp.NewHttpDriverEnv(logger, ctx)

		cachePath = world.TempDir("executor-tmp")

		config = executorinit.ExecutorConfig{
			GardenNetwork:                      "unix",
			GardenAddr:                         "/tmp/garden.sock",
			MemoryMB:                           configuration.Automatic,
			DiskMB:                             configuration.Automatic,
			TempDir:                            "/tmp",
			ReservedExpirationTime:             durationjson.Duration(time.Minute),
			ContainerReapInterval:              durationjson.Duration(time.Minute),
			ContainerInodeLimit:                200000,
			ContainerMaxCpuShares:              0,
			CachePath:                          "/tmp/cache",
			EnableDeclarativeHealthcheck:       false,
			MaxCacheSizeInBytes:                10 * 1024 * 1024 * 1024,
			SkipCertVerify:                     false,
			HealthyMonitoringInterval:          durationjson.Duration(30 * time.Second),
			UnhealthyMonitoringInterval:        durationjson.Duration(500 * time.Millisecond),
			ContainerOwnerName:                 "executor",
			HealthCheckContainerOwnerName:      "executor-health-check",
			CreateWorkPoolSize:                 32,
			DeleteWorkPoolSize:                 32,
			ReadWorkPoolSize:                   64,
			MetricsWorkPoolSize:                8,
			HealthCheckWorkPoolSize:            64,
			MaxConcurrentDownloads:             5,
			GardenHealthcheckInterval:          durationjson.Duration(10 * time.Minute),
			GardenHealthcheckEmissionInterval:  durationjson.Duration(30 * time.Second),
			GardenHealthcheckTimeout:           durationjson.Duration(10 * time.Minute),
			GardenHealthcheckCommandRetryPause: durationjson.Duration(time.Second),
			GardenHealthcheckProcessArgs:       []string{},
			GardenHealthcheckProcessEnv:        []string{},
			GracefulShutdownInterval:           durationjson.Duration(10 * time.Second),
			ContainerMetricsReportInterval:     durationjson.Duration(15 * time.Second),
			EnvoyConfigRefreshDelay:            durationjson.Duration(time.Second),
			EnvoyDrainTimeout:                  durationjson.Duration(15 * time.Minute),
			CSIPaths:                           []string{"/var/vcap/data/csiplugins"},
			CSIMountRootDir:                    "/var/vcap/data/csimountroot",
		}
		config.VolmanDriverPaths = path.Join(componentMaker.VolmanDriverConfigDir(), fmt.Sprintf("node-%d", ginkgoconfig.GinkgoConfig.ParallelNode))
		config.CSIPaths = []string{path.Join(componentMaker.VolmanDriverConfigDir(), fmt.Sprintf("local-node-plugin-%d", ginkgoconfig.GinkgoConfig.ParallelNode))}
		config.GardenNetwork = "tcp"
		config.GardenAddr = componentMaker.Addresses().GardenLinux
		config.HealthyMonitoringInterval = durationjson.Duration(time.Second)
		config.UnhealthyMonitoringInterval = durationjson.Duration(100 * time.Millisecond)
		config.ContainerOwnerName = "executor" + generator.RandomName()
		config.GardenHealthcheckProcessPath = "/bin/sh"
		config.GardenHealthcheckProcessArgs = []string{"-c", "echo", "checking health"}
		config.GardenHealthcheckProcessUser = "vcap"
	})

	Describe("Starting up", func() {
		BeforeEach(func() {
			os.RemoveAll(cachePath)

			executorClient, runner = initializeExecutor(logger, config)

			_, err = gardenClient.Capacity()
			Expect(err).NotTo(HaveOccurred())
		})

		JustBeforeEach(func() {
			process = ginkgomon.Invoke(runner)
		})

		Context("when there are volumes", func() {
			BeforeEach(func() {
				errorResponse := driverClient.Create(env, voldriver.CreateRequest{
					Name: "a-volume",
					Opts: map[string]interface{}{
						"volume_id": "a-volume",
					},
				})
				Expect(errorResponse.Err).To(BeEmpty())

				mountResponse := driverClient.Mount(env, voldriver.MountRequest{
					Name: "a-volume",
				})
				Expect(mountResponse.Err).To(BeEmpty())
			})

			It("deletes the volumes", func() {
				listResponse := driverClient.List(env)
				Expect(listResponse.Err).To(BeEmpty())
				Expect(len(listResponse.Volumes)).To(Equal(1))
				Expect(listResponse.Volumes[0].Mountpoint).To(BeEmpty())
			})
		})
	})

	Context("when volman is not correctly configured", func() {
		BeforeEach(func() {
			invalidDriverPath := ""
			config.VolmanDriverPaths = invalidDriverPath

			executorClient, runner = initializeExecutor(logger, config)

			_, err = gardenClient.Capacity()
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			if executorClient != nil {
				executorClient.Cleanup(logger)
			}

			if process != nil {
				ginkgomon.Interrupt(process)
			}

			os.RemoveAll(cachePath)
		})

		Context("when allocating a container without any volman mounts", func() {
			var (
				guid               string
				allocationRequest  executor.AllocationRequest
				allocationFailures []executor.AllocationFailure
			)

			JustBeforeEach(func() {
				process = ginkgomon.Invoke(runner)
			})

			BeforeEach(func() {
				id, err := uuid.NewV4()
				Expect(err).NotTo(HaveOccurred())
				guid = id.String()

				tags := executor.Tags{"some-tag": "some-value"}

				allocationRequest = executor.NewAllocationRequest(guid, &executor.Resource{}, tags)

				allocationFailures = executorClient.AllocateContainers(logger, []executor.AllocationRequest{allocationRequest})
				Expect(allocationFailures).To(BeEmpty())
			})

			Context("when running the container", func() {
				var (
					runReq executor.RunRequest
				)

				BeforeEach(func() {
					runInfo := executor.RunInfo{
						Action: models.WrapAction(&models.RunAction{
							Path: "/bin/touch",
							User: "root",
							Args: []string{"/tmp"},
						}),
					}
					runReq = executor.NewRunRequest(guid, &runInfo, executor.Tags{})
				})

				It("container start should succeed", func() {
					err := executorClient.RunContainer(logger, &runReq)
					Expect(err).NotTo(HaveOccurred())
					Eventually(containerStatePoller(guid)).Should(Equal(executor.StateCompleted))
					Expect(getContainer(guid).RunResult.Failed).Should(BeFalse())
				})
			})
		})
	})

	Context("when volman is correctly configured", func() {
		BeforeEach(func() {
			executorClient, runner = initializeExecutor(logger, config)

			_, err = gardenClient.Capacity()
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			if executorClient != nil {
				executorClient.Cleanup(logger)
			}

			if process != nil {
				ginkgomon.Interrupt(process)
			}

			os.RemoveAll(cachePath)
		})

		Context("when running an executor in front of garden and volman", func() {
			BeforeEach(func() {
				process = ginkgomon.Invoke(runner)
			})

			Context("when allocating a container", func() {
				var (
					guid               string
					allocationRequest  executor.AllocationRequest
					allocationFailures []executor.AllocationFailure
				)

				BeforeEach(func() {
					id, err := uuid.NewV4()
					Expect(err).NotTo(HaveOccurred())
					guid = id.String()

					tags := executor.Tags{"some-tag": "some-value"}

					allocationRequest = executor.NewAllocationRequest(guid, &executor.Resource{}, tags)

					allocationFailures = executorClient.AllocateContainers(logger, []executor.AllocationRequest{allocationRequest})
					Expect(allocationFailures).To(BeEmpty())
				})

				Context("when running the container", func() {
					var (
						runReq       executor.RunRequest
						volumeId     string
						fileName     string
						volumeMounts []executor.VolumeMount
					)

					BeforeEach(func() {
						fileName = fmt.Sprintf("testfile-%d.txt", time.Now().UnixNano())
						volumeId = fmt.Sprintf("some-volumeID-%d", time.Now().UnixNano())
						someConfig := map[string]interface{}{"volume_id": volumeId}
						volumeMounts = []executor.VolumeMount{executor.VolumeMount{ContainerPath: "/testmount", Driver: "localdriver", VolumeId: volumeId, Config: someConfig, Mode: executor.BindMountModeRW}}
						runInfo := executor.RunInfo{
							VolumeMounts: volumeMounts,
							Privileged:   true,
							Action: models.WrapAction(&models.RunAction{
								Path: "/bin/touch",
								User: "root",
								Args: []string{"/testmount/" + fileName},
							}),
						}
						runReq = executor.NewRunRequest(guid, &runInfo, executor.Tags{})
					})

					Context("when successfully mounting a RW Mode volume", func() {
						BeforeEach(func() {
							err := executorClient.RunContainer(logger, &runReq)
							Expect(err).NotTo(HaveOccurred())
							Eventually(containerStatePoller(guid)).Should(Equal(executor.StateCompleted))
							Expect(getContainer(guid).RunResult.Failed).Should(BeFalse())
						})

						AfterEach(func() {
							err := executorClient.DeleteContainer(logger, guid)
							Expect(err).NotTo(HaveOccurred())

							err = os.RemoveAll(path.Join(componentMaker.VolmanDriverConfigDir(), "_volumes", volumeId))
							Expect(err).ToNot(HaveOccurred())

							files, err := filepath.Glob(path.Join(componentMaker.VolmanDriverConfigDir(), "_volumes", volumeId, fileName))
							Expect(err).ToNot(HaveOccurred())
							Expect(len(files)).To(Equal(0))
						})

						It("can write files to the mounted volume", func() {
							By("we expect the file it wrote to be available outside of the container")
							volmanPath := path.Join(componentMaker.VolmanDriverConfigDir(), "_volumes", volumeId, fileName)
							files, err := filepath.Glob(volmanPath)
							Expect(err).ToNot(HaveOccurred())
							Expect(len(files)).To(Equal(1))
						})

						Context("when a second container using the same volume loads and then unloads", func() {
							var (
								runReq2   executor.RunRequest
								fileName2 string
								guid2     string
							)
							BeforeEach(func() {
								id, err := uuid.NewV4()
								Expect(err).NotTo(HaveOccurred())
								guid2 = id.String()

								tags := executor.Tags{"some-tag": "some-value"}
								allocationRequest2 := executor.NewAllocationRequest(guid2, &executor.Resource{}, tags)
								allocationFailures := executorClient.AllocateContainers(logger, []executor.AllocationRequest{allocationRequest2})
								Expect(allocationFailures).To(BeEmpty())

								fileName2 = fmt.Sprintf("testfile2-%d.txt", time.Now().UnixNano())
								runInfo := executor.RunInfo{
									VolumeMounts: volumeMounts,
									Privileged:   true,
									Action: models.WrapAction(&models.RunAction{
										Path: "/bin/touch",
										User: "root",
										Args: []string{"/testmount/" + fileName2},
									}),
								}
								runReq2 = executor.NewRunRequest(guid2, &runInfo, executor.Tags{})
								err = executorClient.RunContainer(logger, &runReq2)
								Expect(err).NotTo(HaveOccurred())
								Eventually(containerStatePoller(guid2)).Should(Equal(executor.StateCompleted))
								Expect(getContainer(guid2).RunResult.Failed).Should(BeFalse())
								err = executorClient.DeleteContainer(logger, guid2)
								Expect(err).NotTo(HaveOccurred())
							})
							It("can still read files on the mounted volume for the first container", func() {
								volmanPath := path.Join(componentMaker.VolmanDriverConfigDir(), "_volumes", volumeId, fileName)
								files, err := filepath.Glob(volmanPath)
								Expect(err).ToNot(HaveOccurred())
								Expect(len(files)).To(Equal(1))
							})
						})
					})
				})

				Context("when running the container with csi driver", func() {
					var (
						runReq       executor.RunRequest
						csiVolume    string
						volumeId     string
						fileName     string
						volumeMounts []executor.VolumeMount
					)

					BeforeEach(func() {
						fileName = fmt.Sprintf("testfile-%d.txt", time.Now().UnixNano())
						volumeId = fmt.Sprintf("some-volumeID-%d", time.Now().UnixNano())
						csiVolume = fmt.Sprintf("csi-volumeID-%d", time.Now().UnixNano())
						someConfig := map[string]interface{}{"id": csiVolume, "attributes": map[string]string{}}
						volumeMounts = []executor.VolumeMount{executor.VolumeMount{ContainerPath: "/testmount", Driver: node.NODE_PLUGIN_ID, VolumeId: volumeId, Config: someConfig, Mode: executor.BindMountModeRW}}
						runInfo := executor.RunInfo{
							VolumeMounts: volumeMounts,
							Privileged:   true,
							Action: models.WrapAction(&models.RunAction{
								Path: "/bin/touch",
								User: "root",
								Args: []string{"/testmount/" + fileName},
							}),
						}
						runReq = executor.NewRunRequest(guid, &runInfo, executor.Tags{})
					})

					Context("when successfully mounting a RW Mode volume", func() {
						BeforeEach(func() {
							err := executorClient.RunContainer(logger, &runReq)
							Expect(err).NotTo(HaveOccurred())
							Eventually(containerStatePoller(guid)).Should(Equal(executor.StateCompleted))
							Expect(getContainer(guid).RunResult.Failed).Should(BeFalse())
						})

						AfterEach(func() {
							err := executorClient.DeleteContainer(logger, guid)
							Expect(err).NotTo(HaveOccurred())

							err = os.RemoveAll(path.Join(componentMaker.VolmanDriverConfigDir(), "_volumes", volumeId))
							Expect(err).ToNot(HaveOccurred())

							files, err := filepath.Glob(path.Join(componentMaker.VolmanDriverConfigDir(), "_volumes", volumeId, fileName))
							Expect(err).ToNot(HaveOccurred())
							Expect(len(files)).To(Equal(0))
						})

						It("can write files to the mounted volume", func() {
							By("we expect the file it wrote to be available outside of the container")
							volmanPath := path.Join(componentMaker.VolmanDriverConfigDir(), fmt.Sprintf("local-node-volumes-%d", ginkgoconfig.GinkgoConfig.ParallelNode), csiVolume, fileName)
							files, err := filepath.Glob(volmanPath)
							Expect(err).ToNot(HaveOccurred())
							Expect(len(files)).To(Equal(1))
						})

						Context("when a second container using the same volume loads and then unloads", func() {
							var (
								runReq2   executor.RunRequest
								fileName2 string
								guid2     string
							)
							BeforeEach(func() {
								id, err := uuid.NewV4()
								Expect(err).NotTo(HaveOccurred())
								guid2 = id.String()

								tags := executor.Tags{"some-tag": "some-value"}
								allocationRequest2 := executor.NewAllocationRequest(guid2, &executor.Resource{}, tags)
								allocationFailures := executorClient.AllocateContainers(logger, []executor.AllocationRequest{allocationRequest2})
								Expect(allocationFailures).To(BeEmpty())

								fileName2 = fmt.Sprintf("testfile2-%d.txt", time.Now().UnixNano())
								runInfo := executor.RunInfo{
									VolumeMounts: volumeMounts,
									Privileged:   true,
									Action: models.WrapAction(&models.RunAction{
										Path: "/bin/touch",
										User: "root",
										Args: []string{"/testmount/" + fileName2},
									}),
								}
								runReq2 = executor.NewRunRequest(guid2, &runInfo, executor.Tags{})
								err = executorClient.RunContainer(logger, &runReq2)
								Expect(err).NotTo(HaveOccurred())
								Eventually(containerStatePoller(guid2)).Should(Equal(executor.StateCompleted))
								Expect(getContainer(guid2).RunResult.Failed).Should(BeFalse())
								err = executorClient.DeleteContainer(logger, guid2)
								Expect(err).NotTo(HaveOccurred())
							})
							It("can still read files on the mounted volume for the first container", func() {
								volmanPath := path.Join(componentMaker.VolmanDriverConfigDir(), fmt.Sprintf("local-node-volumes-%d", ginkgoconfig.GinkgoConfig.ParallelNode), csiVolume, fileName)
								files, err := filepath.Glob(volmanPath)
								Expect(err).ToNot(HaveOccurred())
								Expect(len(files)).To(Equal(1))
							})
						})

					})
				})

			})
		})
	})
})

func initializeExecutor(logger lager.Logger, config executorinit.ExecutorConfig) (executor.Client, ifrit.Runner) {
	var executorMembers grouper.Members
	var err error
	var executorClient executor.Client
	defaultRootFS := ""
	metronClient, err := loggingclient.NewIngressClient(loggingclient.Config{})
	Expect(err).NotTo(HaveOccurred())
	executorClient, _, executorMembers, err = executorinit.Initialize(logger, config, "", defaultRootFS, metronClient, clock.NewClock())
	Expect(err).NotTo(HaveOccurred())

	return executorClient, grouper.NewParallel(os.Kill, executorMembers)
}
