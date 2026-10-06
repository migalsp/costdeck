# Changelog

## [1.5.4](https://github.com/migalsp/costdeck/compare/v1.5.3...v1.5.4) (2026-10-06)


### Bug Fixes

* **scaling:** move on from a stage that misses its timeout ([8148b55](https://github.com/migalsp/costdeck/commit/8148b5501a282d79f9a6d1e0cc326c7132716872))
* **scaling:** move on from a stage that misses its timeout ([b14ff64](https://github.com/migalsp/costdeck/commit/b14ff64ed3f1ae0562a0d848e4c3952654927ef0))

## [1.5.3](https://github.com/migalsp/costdeck/compare/v1.5.2...v1.5.3) (2026-10-06)


### Bug Fixes

* **scaling:** keep a dependent that is scaling down from starting a stopped dependency ([235bb01](https://github.com/migalsp/costdeck/commit/235bb015cbad20174bd40c4f00a6fbb323d7be3f))
* **ui:** allow deleting single-namespace schedules ([6fde69e](https://github.com/migalsp/costdeck/commit/6fde69e67bece404ed15dea952e94f83db41dcd9))

## [1.5.2](https://github.com/migalsp/costdeck/compare/v1.5.1...v1.5.2) (2026-10-06)


### Bug Fixes

* **scaling:** start a namespace's workload stages in order and stop them in reverse ([52619ba](https://github.com/migalsp/costdeck/commit/52619ba212d34dda1d690c3e65c512c8ab64d190))

## [1.5.1](https://github.com/migalsp/costdeck/compare/v1.5.0...v1.5.1) (2026-10-06)


### Bug Fixes

* **pricing:** estimate on-premises storage and align the default rates with list prices ([1ee4ba0](https://github.com/migalsp/costdeck/commit/1ee4ba0abfdab9f6db32ae2319f4f7be52ac9161))

## [1.5.0](https://github.com/migalsp/costdeck/compare/v1.4.0...v1.5.0) (2026-10-06)


### Features

* **budgets:** let one budget cover several namespaces ([b4a6adb](https://github.com/migalsp/costdeck/commit/b4a6adbbaeb0dd47c8a4c8f37aec698551a81f6f))


### Bug Fixes

* **ai:** send tool schemas that strict OpenAI-compatible servers accept ([02fe500](https://github.com/migalsp/costdeck/commit/02fe500699fa001df49433923ae8eaa4307a93a9))
* **auth:** check the built-in admin's password against a bcrypt hash ([34cbada](https://github.com/migalsp/costdeck/commit/34cbada544d0a6d936a9fe73a3cccf86275e2d45))
* **auth:** keep the post-login redirect on this site when its path hides a tab ([b86c44b](https://github.com/migalsp/costdeck/commit/b86c44b0cad9ccd6d6e1e88a4ec17bd97fc79fee))

## [1.4.0](https://github.com/migalsp/costdeck/compare/v1.3.0...v1.4.0) (2026-10-06)


### Features

* **ai:** rebuild the assistant around tools, confirmations and real providers ([11feb6a](https://github.com/migalsp/costdeck/commit/11feb6ac52992b0f7029d6c26c6d2fc9d4423b16))
* **api:** report pod readiness, restarts and the reason a pod is unhealthy ([408aeb4](https://github.com/migalsp/costdeck/commit/408aeb4bf7285f2931f7416ea1d28ebfd7c3b4eb))
* **api:** serve the OpenAPI spec as JSON and keep it in step with the routes ([00ce4f5](https://github.com/migalsp/costdeck/commit/00ce4f57346dfb50a082017f4c3c1a671c73f147))
* **auth:** add Microsoft Entra ID single sign-on and role-based access ([2870fcf](https://github.com/migalsp/costdeck/commit/2870fcfbad7f4e89235341c647e69924602b600e))
* **auth:** manage local users and their roles from the dashboard ([9bd4591](https://github.com/migalsp/costdeck/commit/9bd4591da34cec40c22318400808158fa4906385))
* **billing:** reconcile cost estimates with the AWS, Azure or Google Cloud bill, discounts and spot prices included ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **budgets:** alert on monthly budgets per cluster, namespace, team or environment, and on cost anomalies, in Webex ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **cloud:** scale Azure and Google Cloud resources alongside namespaces ([9128fc6](https://github.com/migalsp/costdeck/commit/9128fc6e2e6a6b4036cc7c58b45b32dc3bfdaee1))
* **cost:** price nodes with the AWS Price List API and support custom rates ([704e6a0](https://github.com/migalsp/costdeck/commit/704e6a0f6d5d83a26c5952bea9825b9ad640a3e4))
* **finops:** add a cost overview and rework Namespace Insights around it ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **finops:** price persistent volumes and load balancers, charge them to their namespace and find unused ones ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **helm:** harden the chart and expose Prometheus metrics ([6616851](https://github.com/migalsp/costdeck/commit/6616851077b53e1944f16be9c81bb9bdef212ca0))
* **helm:** name the built-in admin "costdeck" on new installs ([9bd4591](https://github.com/migalsp/costdeck/commit/9bd4591da34cec40c22318400808158fa4906385))
* **helm:** spread replicas over nodes and add a PodDisruptionBudget when replicaCount &gt; 1 ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **mcp:** give the assistant and MCP clients a cost overview tool ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **mcp:** serve MCP over Streamable HTTP with API-token authentication ([d07e80b](https://github.com/migalsp/costdeck/commit/d07e80b60b35f5035842ff709d1fce363c93c7a2))
* **metrics:** report estimated savings and export scaling state to Prometheus ([3928baa](https://github.com/migalsp/costdeck/commit/3928baa4af2b163fd807a6ae87da6df8d7a42723))
* **nodes:** recommend instance types, node counts, arm64 and spot capacity per node pool ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **pricing:** price AKS nodes from the public Azure Retail Prices API ([9128fc6](https://github.com/migalsp/costdeck/commit/9128fc6e2e6a6b4036cc7c58b45b32dc3bfdaee1))
* **reports:** post a weekly or monthly cost digest to Webex and keep a history of reports ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **rightsizing:** replace the Optimize button with read-only right-sizing advice ([362b291](https://github.com/migalsp/costdeck/commit/362b291713c937ef09cac85c759d2a8c862e6a35))
* **scaling:** add dependencies between ScalingGroups ([ee6ccc8](https://github.com/migalsp/costdeck/commit/ee6ccc871b6347d4a34f5a0f64ac1a81d9ef8566))
* **scaling:** show who controls a group and when it changes next ([aeb9ed8](https://github.com/migalsp/costdeck/commit/aeb9ed8a706141fd02299e58b0f912f430834ad2))
* **scaling:** suspend CronJobs and pause KEDA autoscaling while scaled down ([b5774ca](https://github.com/migalsp/costdeck/commit/b5774cafcb58a398c11ac9eb9ce41417682cd12b))
* **ui:** configure Azure and Google Cloud and schedule their resources ([3e6ff0c](https://github.com/migalsp/costdeck/commit/3e6ff0c23ade845c03c7d0cceb7cb5cb1600948f))
* **ui:** group the settings into sections, each with its own save button ([70297f8](https://github.com/migalsp/costdeck/commit/70297f82dce718db3a8343b9d1596343d3acfca8))
* **ui:** name what each colour in bars and timelines means on hover ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **ui:** one visual language across the dashboard ([6e41cce](https://github.com/migalsp/costdeck/commit/6e41cce43ca103f66275144180d3674103fbdad6))
* **ui:** open a schedule to see its activity and arrange its start order ([6e41cce](https://github.com/migalsp/costdeck/commit/6e41cce43ca103f66275144180d3674103fbdad6))
* **ui:** rebuild the Cluster Node Map with cost, bin packing and the pods on each node ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **ui:** rewrite the documentation as task-based guides with a live API reference ([f820aa0](https://github.com/migalsp/costdeck/commit/f820aa07143b079b5ca8749a56249d8174636436))
* **ui:** show a schedule's live pipeline and edit its start order with the schedule ([408aeb4](https://github.com/migalsp/costdeck/commit/408aeb4bf7285f2931f7416ea1d28ebfd7c3b4eb))
* **ui:** show the leader, every replica and the leader's logs on the health page ([b63ea9d](https://github.com/migalsp/costdeck/commit/b63ea9d7efb62aac41bc244ba32a05ada76a388e))
* **ui:** simplify scaling setup with presets, a week timeline and one-screen editing ([62bc828](https://github.com/migalsp/costdeck/commit/62bc8287580788a8c07a1414b6eeedc7c9d5770f))
* **ui:** use the Cost Deck brand mark across the dashboard ([18bd177](https://github.com/migalsp/costdeck/commit/18bd177e7c0f1a478aa1f157d663a2f5a3da92e9))
* **webex:** announce finished scaling transitions in a Webex space ([7228fc6](https://github.com/migalsp/costdeck/commit/7228fc630832ba822918dfccbad26457a0383dcd))


### Bug Fixes

* **ai:** refuse cloud metadata addresses for every AI provider, the local one included ([60d23ff](https://github.com/migalsp/costdeck/commit/60d23ff1d5e52b2208de3cf10760ad8e3b847e70))
* **api:** remove a data race on the operator health history ([4bc8c4b](https://github.com/migalsp/costdeck/commit/4bc8c4bd3c55615af5efb7bc4793994f8f4eea80))
* **api:** serve the API and dashboard from every replica ([4bc8c4b](https://github.com/migalsp/costdeck/commit/4bc8c4bd3c55615af5efb7bc4793994f8f4eea80))
* **api:** stop seeding a manual override when groups or configs are created ([4bc8c4b](https://github.com/migalsp/costdeck/commit/4bc8c4bd3c55615af5efb7bc4793994f8f4eea80))
* **auth:** never fall back to anonymous admin when credentials are missing ([4c7871b](https://github.com/migalsp/costdeck/commit/4c7871b2f085e9f5bb28547912e9acb079ab9c79))
* **auth:** rate-limit sign-in attempts by the proxy-observed address, not a client-supplied one ([4c7871b](https://github.com/migalsp/costdeck/commit/4c7871b2f085e9f5bb28547912e9acb079ab9c79))
* **auth:** refuse cross-site writes made with the session cookie ([f2ba1b7](https://github.com/migalsp/costdeck/commit/f2ba1b77f11bde177596f133e442cf0876d9b62e))
* **auth:** require an HTTPS Entra authority host ([5f6baa8](https://github.com/migalsp/costdeck/commit/5f6baa806a0dbd7d6e6ed0892377ef6686168373))
* **auth:** trust only group mappings for multi-tenant Entra ID registrations ([0a1429b](https://github.com/migalsp/costdeck/commit/0a1429b8c4871da8f9c56efe161a7627aab9b439))
* **cloud:** keep cloud credentials on the API host they were issued for ([ac7400f](https://github.com/migalsp/costdeck/commit/ac7400f33bc0f30bd329a2f3770fdf0728b6f811))
* **crd:** show the next schedule change as a timestamp in kubectl get ([40a3610](https://github.com/migalsp/costdeck/commit/40a36100d4c82fb49a1da2c4b3befe2442776b28))
* **deps:** patch grpc, OpenTelemetry, Go stdlib and UI advisories ([fa1f60c](https://github.com/migalsp/costdeck/commit/fa1f60c16c817886c2dd11df26d6da771dd0e94e))
* **discovery:** forget namespaces that were deleted ([422885c](https://github.com/migalsp/costdeck/commit/422885cc6477ebc0cac4d82e60e7f1d71f013c71))
* **helm:** default to the public GHCR image and keep the Ingress off unless configured ([6616851](https://github.com/migalsp/costdeck/commit/6616851077b53e1944f16be9c81bb9bdef212ca0))
* **helm:** keep the CRDs, and with them all schedules and settings, on helm uninstall ([24f2d3f](https://github.com/migalsp/costdeck/commit/24f2d3f831b494cb6d22037221b8333edb41409c))
* **install:** include the CostDeckConfig CRD in the kustomize bundle ([2e3561c](https://github.com/migalsp/costdeck/commit/2e3561c3d2442c90c121c609012842492ea18aa9))
* **metrics:** keep the VictoriaMetrics endpoint away from cloud metadata addresses ([60d23ff](https://github.com/migalsp/costdeck/commit/60d23ff1d5e52b2208de3cf10760ad8e3b847e70))
* **metrics:** make the VictoriaMetrics integration actually work ([584ad1b](https://github.com/migalsp/costdeck/commit/584ad1b729361052238c59f03e093bc5aea3f366))
* **rbac:** stop requiring cluster-wide access to Secrets ([2c9e57e](https://github.com/migalsp/costdeck/commit/2c9e57e6ccca48b2691e2d03ae8729ae3df38cc6))
* **scaling:** correct sequence matching, replica restore and readiness ([e2ab1a6](https://github.com/migalsp/costdeck/commit/e2ab1a6b2e2e479f9b036af553890707d9751e28))
* **scaling:** keep each workload's original replica count on the workload itself ([fbfb877](https://github.com/migalsp/costdeck/commit/fbfb877067480c606abac7c9c2f4c22ed3b21ebe))
* **scaling:** report the real skip-on-timeout window in stage events ([e2ab1a6](https://github.com/migalsp/costdeck/commit/e2ab1a6b2e2e479f9b036af553890707d9751e28))
* **scaling:** use the configured AWS credentials for external targets ([e2ab1a6](https://github.com/migalsp/costdeck/commit/e2ab1a6b2e2e479f9b036af553890707d9751e28))
* **ui:** cap the give-up timeout at the 30 minutes the API accepts ([a0217d4](https://github.com/migalsp/costdeck/commit/a0217d46892e9d2358eff4a31e34ca7730a339c4))
* **ui:** keep client-side routes working after a reload ([4bc8c4b](https://github.com/migalsp/costdeck/commit/4bc8c4bd3c55615af5efb7bc4793994f8f4eea80))
* **ui:** label the OnDemand scaling mode instead of printing the raw value ([7368b6a](https://github.com/migalsp/costdeck/commit/7368b6a1f6dc802e084d638b166d81d9349a2ef5))
* **ui:** offer workload rules only for namespaces that have a schedule ([bdd5621](https://github.com/migalsp/costdeck/commit/bdd5621c1c48434e04426df58b4221a8fd3bf221))
* **ui:** order pipeline stages for scale-down when the schedule, not a manual override, scales a group down ([7368b6a](https://github.com/migalsp/costdeck/commit/7368b6a1f6dc802e084d638b166d81d9349a2ef5))
* **ui:** show a power-off icon on "Scale down now" instead of an empty square ([31deea4](https://github.com/migalsp/costdeck/commit/31deea40265bc0d808c6c7a9003cc5dedfb8c898))
* **ui:** stop pinning a namespace forever from its details page ([2870fcf](https://github.com/migalsp/costdeck/commit/2870fcfbad7f4e89235341c647e69924602b600e))
* **ui:** stop showing the previous schedule's status right after saving an edit ([6e41cce](https://github.com/migalsp/costdeck/commit/6e41cce43ca103f66275144180d3674103fbdad6))
* **webex:** accept scaling commands only from members of the configured space ([8b726ce](https://github.com/migalsp/costdeck/commit/8b726ce239b742842c950a68e103c8d89421d6a5))
* **webex:** answer every command and surface why replies fail ([e0bc021](https://github.com/migalsp/costdeck/commit/e0bc02126dc696a9ebd86248fe95e565534400cb))


### Performance Improvements

* **api:** drop managedFields from Kubernetes objects in API responses ([c72be55](https://github.com/migalsp/costdeck/commit/c72be55c360b8008ba6b49b3dc385b92b30e35d5))
* cut the operator's steady load on the Kubernetes API ([b513ac0](https://github.com/migalsp/costdeck/commit/b513ac018c5a74a7be82f64958950c97284e8ff6))

## [1.3.0](https://github.com/migalsp/costdeck/compare/v1.2.1...v1.3.0) (2026-08-12)


### Features

* **scaling:** add continuous multi-day windows and expiring overrides ([f34ec4b](https://github.com/migalsp/costdeck/commit/f34ec4b42ff6b5742202f741f4ec4b2f1bda8b77))
* **ui:** edit continuous, overnight and multiple schedule windows ([d426549](https://github.com/migalsp/costdeck/commit/d426549671d5fbfc900a1d8f56de9abf47dab5a6))


### Bug Fixes

* **scaling:** resolve non-UTC schedule timezones inside the container ([0e66a4c](https://github.com/migalsp/costdeck/commit/0e66a4cc9545c052d8f16201e9e9f72974ac1b80))
* **ui:** allow clearing a manual override from the dashboard ([c148b10](https://github.com/migalsp/costdeck/commit/c148b10cd82717aec23a98369898b8bf07f72868))

## [1.2.1](https://github.com/migalsp/costdeck/compare/v1.2.0...v1.2.1) (2026-06-06)


### Bug Fixes

* Uncontrolled data used in network request ([943456f](https://github.com/migalsp/costdeck/commit/943456fb1cdebc5846c7afaabc996fbaeeffe40d))

## [1.2.0](https://github.com/migalsp/costdeck/compare/v1.1.2...v1.2.0) (2026-06-06)


### Features

* major FinOps and AI capabilities update, Go 1.26 bump, and UI overhaul ([57239ee](https://github.com/migalsp/costdeck/commit/57239ee97eb124e8a6715bf183ba626fecd886a5))
* major FinOps and AI capabilities update, Go 1.26 bump, and UI overhaul ([cb5c781](https://github.com/migalsp/costdeck/commit/cb5c781ef5e4b30f7fa4066d79ab1b1621b07479))


### Bug Fixes

* resolve SSRF vulnerabilities, chat UI bugs, and improve AI reporting ([a4cef23](https://github.com/migalsp/costdeck/commit/a4cef23d0aeb5cef516edd59446d237bdd223926))

## [1.1.2](https://github.com/migalsp/costdeck/compare/v1.1.1...v1.1.2) (2026-03-27)


### Bug Fixes

* **scaling:** ignore succeeded pods during scale-down sequencing ([877012a](https://github.com/migalsp/costdeck/commit/877012ad3086a8a20c0b3d8e00b55ad4a36b4b69))

## [1.1.1](https://github.com/migalsp/costdeck/compare/v1.1.0...v1.1.1) (2026-03-27)


### Bug Fixes

* configure operator deployment with updated API version display. ([08b4d43](https://github.com/migalsp/costdeck/commit/08b4d4319a77311c8f3a9251ef9aa2ef07953d47))
* enhance UI metric type definitions. ([5be65dc](https://github.com/migalsp/costdeck/commit/5be65dc4864c15dc01b82a6950f2040e02085996))

## [1.1.0](https://github.com/migalsp/costdeck/compare/v1.0.0...v1.1.0) (2026-03-18)


### Features

* costdeck init release ([#1](https://github.com/migalsp/costdeck/issues/1)) ([3f17148](https://github.com/migalsp/costdeck/commit/3f171485cf4ba62a8f3d95d2422dbf2d096a3e3b))
