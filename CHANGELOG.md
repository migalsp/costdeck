# Changelog

## [1.7.0](https://github.com/migalsp/costdeck/compare/v1.6.3...v1.7.0) (2026-11-03)


### Features

* **auth:** grant OIDC roles only through a role mapping or costdeck. names ([26d06d5](https://github.com/migalsp/costdeck/commit/26d06d5901d44cff7ba9611874e4df6be35b710e))
* JVM right-sizing, performance risks, prewarm and a hardening pass ([44292d1](https://github.com/migalsp/costdeck/commit/44292d18f070f3dc899d5a3a603b3ab3e80bb7e6))
* **rightsizing:** advise on the JVM heap of Java containers ([51cd10b](https://github.com/migalsp/costdeck/commit/51cd10b8e489d399348ac9d4dc58b2ee9c6476b9))
* **rightsizing:** bound and gate advice with RightsizingPolicy ([2d94f7e](https://github.com/migalsp/costdeck/commit/2d94f7e019378824baf77e9fe6cf056459f7c072))
* **risks:** alert channels about new critical performance risks ([197f4b7](https://github.com/migalsp/costdeck/commit/197f4b7ed3fe59b2af4c13bc7d74f51dd61160f6))
* **risks:** flag namespaces that take two minutes or more to wake up ([969037c](https://github.com/migalsp/costdeck/commit/969037c947dbef5c8fdf80277759468567af42ab))
* **risks:** list performance risks and hold right-sizing under pressure ([675147e](https://github.com/migalsp/costdeck/commit/675147ef47912671fcd2519ff19b19baa93b80a0))
* **scaling:** prewarm namespaces by how long they take to wake up ([969037c](https://github.com/migalsp/costdeck/commit/969037c947dbef5c8fdf80277759468567af42ab))
* **site:** show the repository's GitHub stars on the landing page ([48b2329](https://github.com/migalsp/costdeck/commit/48b2329b61d485011520b3fefd8be2f3162455da))


### Bug Fixes

* **ai:** flag cut-off Anthropic tool input and report overloads as temporary ([a369ae6](https://github.com/migalsp/costdeck/commit/a369ae61c0a992ff1f819e0ae3b8d5b9588663b8))
* **api:** answer confirmed assistant actions with client errors where they are ([2e3c228](https://github.com/migalsp/costdeck/commit/2e3c228a5d4b7d7c4613e2e0f595a2a0d6eea7cc))
* **api:** bound the schedules a preview or a save has to work out ([740131b](https://github.com/migalsp/costdeck/commit/740131bd407e2103a099f7033a88587372ff7367))
* **api:** check right-sizing policies as they are when applying advice ([e96c634](https://github.com/migalsp/costdeck/commit/e96c634023b85efa3e8f162e90a43427b3bab9fe))
* **api:** count the measured prewarm in previews and estimates ([c912728](https://github.com/migalsp/costdeck/commit/c912728ea35533ad2147ce59d61936fc66906aaa))
* **api:** drop the stored token of the last removed cluster ([25ab72c](https://github.com/migalsp/costdeck/commit/25ab72c7813828391e1a558b4c8b78ff28200764))
* **api:** keep the open risks of a namespace that cannot be assessed ([2a849b6](https://github.com/migalsp/costdeck/commit/2a849b67e6dd45e8c79e53e8f4556dfb9e2c7920))
* **api:** keep workload scaling and right-sizing out of protected namespaces ([81f2b87](https://github.com/migalsp/costdeck/commit/81f2b8780ae452960cf1e0b242aff68dc9fb652f))
* **api:** list a local model server's models before one is chosen ([2e3c228](https://github.com/migalsp/costdeck/commit/2e3c228a5d4b7d7c4613e2e0f595a2a0d6eea7cc))
* **api:** name the schedule that owns a namespace as the operator does ([8d037c3](https://github.com/migalsp/costdeck/commit/8d037c32b1b782af916084232b6d30db6c2ff41f))
* **api:** pin swagger-ui with integrity hashes and send a content security policy ([a3e2265](https://github.com/migalsp/costdeck/commit/a3e2265a844ee343c7165725f24b787415d3c31f))
* **api:** refuse a cloud connection test whose request cannot be read ([7898ee1](https://github.com/migalsp/costdeck/commit/7898ee1ec81ebc3dac0a528b15b44eab098c1170))
* **api:** run a replayed Webex webhook delivery only once ([924c26c](https://github.com/migalsp/costdeck/commit/924c26c258d72fe1a7a08d39a70c4fa803d9a4d9))
* **api:** stop per-namespace caches growing on made-up namespaces ([fce6e52](https://github.com/migalsp/costdeck/commit/fce6e529cb8c79b4f1231f804c0b8a316762c66e))
* **api:** write credentials only when the whole settings save is valid ([c8a2efd](https://github.com/migalsp/costdeck/commit/c8a2efd6732a7eed3395dcaa015ee0dc29764040))
* **auth:** limit failed sign-ins per account as well as per address ([75cbc5e](https://github.com/migalsp/costdeck/commit/75cbc5e1620aef47714c91c5a09e51220cb0c911))
* **auth:** refuse cross-site writes to the login and logout endpoints ([75cbc5e](https://github.com/migalsp/costdeck/commit/75cbc5e1620aef47714c91c5a09e51220cb0c911))
* **auth:** require sign-in once OIDC is on, even with COSTDECK_AUTH_DISABLED ([75cbc5e](https://github.com/migalsp/costdeck/commit/75cbc5e1620aef47714c91c5a09e51220cb0c911))
* **cleanup:** keep warning once the hour's deletions are used up ([90c60b6](https://github.com/migalsp/costdeck/commit/90c60b69a2555479c8eb787897729186af0b9c84))
* **cleanup:** warn before deleting a namespace that expired unwarned ([e4399c0](https://github.com/migalsp/costdeck/commit/e4399c07322fd346d79f3991ac1d38afff0d3a21))
* **controller:** forget deleted schedules and survive an EC2 answer without state ([f1a9b16](https://github.com/migalsp/costdeck/commit/f1a9b1616489c4b01d48f1d965609876f9cce1f8))
* **controller:** hand a namespace between a group and its config at once ([c976646](https://github.com/migalsp/costdeck/commit/c97664605cac8610553b9e5234f23160df57366e))
* **controller:** hand a namespace over to the group that takes it ([1689ead](https://github.com/migalsp/costdeck/commit/1689eadc43d07de3cacadefd121ac3dc4470bbab))
* **controller:** leave a namespace that moves to another group to its new owner ([44a3a27](https://github.com/migalsp/costdeck/commit/44a3a27b9d18c811f60ab3226307da2c384a19c0))
* **controller:** leave what other schedules paused when a dry run is deleted ([aeab558](https://github.com/migalsp/costdeck/commit/aeab5585e787dcd4b3023da3a08b845e62272547))
* **controller:** release a namespace that leaves a group even without replica records ([afd8840](https://github.com/migalsp/costdeck/commit/afd88403228a5c5508f2ce0de19a3393cf3053d8))
* **controller:** restore every cloud target a group stopped ([1683432](https://github.com/migalsp/costdeck/commit/1683432c241375e6927586ba02f76920aaf47419))
* **controller:** restore namespaces removed just before a group is deleted ([58fce47](https://github.com/migalsp/costdeck/commit/58fce47795fede0205dd91ef54e4480bbf28cf60))
* **controller:** restore only recorded cloud targets, and wait until they run ([8988ce5](https://github.com/migalsp/costdeck/commit/8988ce5442a0340eccb65abf009ac47d61bde040))
* **controller:** start nothing when a dry-run schedule is deleted ([6705ad4](https://github.com/migalsp/costdeck/commit/6705ad43ffc41eb8c1710e12e9fe63303e1ccd77))
* **controller:** start only the cloud targets a deleted group scales ([385c782](https://github.com/migalsp/costdeck/commit/385c7825dc3254a219b2108527c0df14df40faac))
* **controller:** stop CostDeckConfig re-checking providers on its own status writes ([2fb2945](https://github.com/migalsp/costdeck/commit/2fb2945cca36d641467cd99e5a5895d9e3e4b54c))
* **controller:** tell recorded cloud targets apart by provider, type and region ([3498204](https://github.com/migalsp/costdeck/commit/3498204a67748b702a8f7998000470031e9ed3a8))
* **deps:** build with Go 1.27.3 for the crypto/x509 security fix ([01c0b9b](https://github.com/migalsp/costdeck/commit/01c0b9b29dd315c1e251c955d05262fcc17b4a74))
* **finops:** count pod requests as the scheduler does, in one place ([a098b7b](https://github.com/migalsp/costdeck/commit/a098b7b34b00dad81642844a8e35cd4efe9fb755))
* **finops:** stop exporting the series of the last removed budget ([0aca21f](https://github.com/migalsp/costdeck/commit/0aca21fc1050515a0fcb27b9b2ac1dc3977d66e3))
* **gitrepo:** edit requests under an empty resources or requests key ([b2c0f18](https://github.com/migalsp/costdeck/commit/b2c0f180feb2073597596b4dce67829ce2839b36))
* **gitrepo:** keep a GitLab merge request from overwriting a newer manifest ([b2c0f18](https://github.com/migalsp/costdeck/commit/b2c0f180feb2073597596b4dce67829ce2839b36))
* **manager:** log in production mode and honour --enable-http2 ([c412b5b](https://github.com/migalsp/costdeck/commit/c412b5bffbb603160e783564697353b1bb0db65e))
* **netguard:** check proxied targets and keep cloud AI providers off loopback ([b11a75f](https://github.com/migalsp/costdeck/commit/b11a75f96895158357106a88f37252f590aa22b5))
* **optimize:** never leave an applied change without its record ([e0fbdd3](https://github.com/migalsp/costdeck/commit/e0fbdd3f4e90cac2abc06ba218ac57b353c502c0))
* **optimize:** retry a record whose first write lost the race ([39db04f](https://github.com/migalsp/costdeck/commit/39db04fc803755e1be64b90b59dc428988184428))
* **rightsizing:** forbid applying advice while the policies cannot be read ([9dd0801](https://github.com/migalsp/costdeck/commit/9dd0801eb63ce475e75155a806f50c5baca35315))
* **rightsizing:** ignore heap windows a series has only just begun ([0374c11](https://github.com/migalsp/costdeck/commit/0374c1101e514c8adb7f1b19075314a6e46cba9f))
* **rightsizing:** mark only the main container Java by annotation ([248074b](https://github.com/migalsp/costdeck/commit/248074bbcfbaecd85662f4defd030c1e6efee35b))
* **rightsizing:** stop attributing other workloads' pods to a Deployment ([5f3b818](https://github.com/migalsp/costdeck/commit/5f3b818dd4ad4a57b0f0d949623b7859279665b0))
* **risks:** count a container waiting for its next retry as crash-looping ([ec6053a](https://github.com/migalsp/costdeck/commit/ec6053a47e225c6bd73172d6761bda2f959f3b55))
* **risks:** keep the risks of workloads excluded from right-sizing ([f788cc4](https://github.com/migalsp/costdeck/commit/f788cc4322e202785b7c0bde4f6a0dbf8ad87a5b))
* **risks:** leave a pod that is being deleted out of crash loops ([fe3afe9](https://github.com/migalsp/costdeck/commit/fe3afe91dc682c408e59069d095327ac5de527e3))
* **risks:** see a crash loop in a pod still pending on another container ([e2894f2](https://github.com/migalsp/costdeck/commit/e2894f218acdcbbedbbdb1cad96d47c94218ed1f))
* **scaling:** keep a dry-run group from taking namespaces over ([1c3c39a](https://github.com/migalsp/costdeck/commit/1c3c39a722d5f50f19abb8a5957c5e87a045f2bf))
* **scaling:** let a single-namespace schedule wait for each stage ([f47e9af](https://github.com/migalsp/costdeck/commit/f47e9af19adef30d4ee9104596c538636bdbfe56))
* **scaling:** pause only GitOps objects that manage nothing outside the schedule ([fbc5b0e](https://github.com/migalsp/costdeck/commit/fbc5b0e6e69714dad1b04cddf84a055204beada2))
* **scaling:** restore a workload partly started by hand when its stage comes up ([daa5e4d](https://github.com/migalsp/costdeck/commit/daa5e4d94838a8c54461882369c0eaa3436ba457))
* **scaling:** restore namespaces when their schedule is deleted or drops them ([4f11360](https://github.com/migalsp/costdeck/commit/4f11360aa936df1808d902858a307ddedc4e051b))
* **scaling:** scale only a group's own namespaces from its sequence ([50a8905](https://github.com/migalsp/costdeck/commit/50a8905723f9605efd7c2d7dd29be1c4df798d36))
* **scaling:** use the configured AWS region with pod identity ([10a7ef9](https://github.com/migalsp/costdeck/commit/10a7ef973e627cc3295c290ea9b6dc4100d9ca1b))
* **sleep:** serve the sleep page from a Service that reaches nothing else ([20b374a](https://github.com/migalsp/costdeck/commit/20b374a182048be9570a69e43fba31fc06b71074))
* **ui:** flag exactly the hibernate rules the operator refuses ([864f132](https://github.com/migalsp/costdeck/commit/864f1321ddfa804715c6d3024f9082c89ba7ab96))
* **ui:** keep refused scaling and user actions on screen until closed ([7e855f3](https://github.com/migalsp/costdeck/commit/7e855f37abc9acdae3a49b84636714edf7858fd4))
* **ui:** keep the fallback minutes of an automatic prewarm on save ([2755771](https://github.com/migalsp/costdeck/commit/275577121da83a05730bb62355277f13a132390a))
* **ui:** label form fields, name dialogs and report token failures ([4a2378e](https://github.com/migalsp/costdeck/commit/4a2378e2189799a2986cd26004e4e9b3b610c048))
* **ui:** show a saved prewarm lead that is not one of the presets ([50d1cb2](https://github.com/migalsp/costdeck/commit/50d1cb20d0e7f8532d973b561a66dd224ff719d1))
* **ui:** show performance risk alerts as risks in the alert list ([5004fdf](https://github.com/migalsp/costdeck/commit/5004fdfa3b78448ca9f9d7f28e55d861e4ab226a))
* **ui:** show the operator logs only to roles that may read them ([40f6af0](https://github.com/migalsp/costdeck/commit/40f6af05b7226cb847bdefbb9490eb9b731a00d8))

## [1.6.3](https://github.com/migalsp/costdeck/compare/v1.6.2...v1.6.3) (2026-10-31)


### Bug Fixes

* **estimate:** count negative replicas, requests and storage as zero ([ea60912](https://github.com/migalsp/costdeck/commit/ea6091274639b3fee7e34e939976d53a9869144f))
* **estimate:** read a Rollout's replica count ([8c16368](https://github.com/migalsp/costdeck/commit/8c163682800abda4bba172a070d7ee91c7e38a81))
* **estimate:** warn about every negative quantity Kubernetes rejects ([85bc8ea](https://github.com/migalsp/costdeck/commit/85bc8ea0deb8bf7d5949c715d573852b1ea6780c))
* fuzz the schedules and estimates, and fix what the fuzzing found ([6a9bb03](https://github.com/migalsp/costdeck/commit/6a9bb034593d413ebf169a2d9535151e8ff951a4))
* **scaling:** find the next change after an exception longer than a week ([2ea793e](https://github.com/migalsp/costdeck/commit/2ea793ee1f4a8b9eebdde53a562a3bc4cfcb66f4))
* **scaling:** read only the HH:MM times the CRD and the dashboard accept ([24297d2](https://github.com/migalsp/costdeck/commit/24297d2c226f1c0266b08be4fac926dbf2ed39a6))

## [1.6.2](https://github.com/migalsp/costdeck/compare/v1.6.1...v1.6.2) (2026-10-29)


### Bug Fixes

* address the OpenSSF Scorecard findings and the AI budget scenario race ([#66](https://github.com/migalsp/costdeck/issues/66)) ([b4077d5](https://github.com/migalsp/costdeck/commit/b4077d51f148d416e5f90a25725aa3c4347dd8ef))
* **deps:** update go-jose to 4.1.6 for GHSA-mgcc-cww5-fp2q ([265e831](https://github.com/migalsp/costdeck/commit/265e831f0dc6b1f154d9238d7d7f83cf436f9ad4))

## [1.6.1](https://github.com/migalsp/costdeck/compare/v1.6.0...v1.6.1) (2026-10-27)


### Bug Fixes

* **estimate:** pass fail-above to the action's script through the environment ([dea092c](https://github.com/migalsp/costdeck/commit/dea092c5bf9ef172c0ae0d43067181c1e097396f))
* **notifications:** write email headers from parsed addresses ([209fb56](https://github.com/migalsp/costdeck/commit/209fb56c55a5e1eb40ae2d295613411aae76a631))
* **reports:** write a digest's period from constants ([6bb56bb](https://github.com/migalsp/costdeck/commit/6bb56bb40dc160273167fed86bdcb2a0c6bca4c1))
* **reports:** write a digest's period from constants ([8dfd33e](https://github.com/migalsp/costdeck/commit/8dfd33e5ea762e1f87376b4f511b89b6dd1aa379))

## [1.6.0](https://github.com/migalsp/costdeck/compare/v1.5.7...v1.6.0) (2026-10-23)


### Features

* **ai:** cap the assistant's tokens with a monthly budget ([8ca54e8](https://github.com/migalsp/costdeck/commit/8ca54e8e08a66b84b1315572a66a79801a952d68))
* **auth:** sign in with Keycloak or any OpenID Connect provider ([a9e306f](https://github.com/migalsp/costdeck/commit/a9e306f723ed72bcd38f05adb99a0bf8e823834a))
* **cleanup:** delete preview namespaces after a time to live ([bd7011a](https://github.com/migalsp/costdeck/commit/bd7011aa2ded3ccb692168648aa886d0f583b284))
* **estimate:** price a pull request's change of manifests before it is merged ([5d75a09](https://github.com/migalsp/costdeck/commit/5d75a09f2d2c7a87bf53814c92855c033d66663b))
* **finops:** charge a period's cost to teams, with shared and idle cost, as CSV ([1e21aed](https://github.com/migalsp/costdeck/commit/1e21aed383bc6a64e306fea5c5f8d962a0abfc14))
* **finops:** check whether a scale-down freed its nodes, and name what keeps them ([08803c9](https://github.com/migalsp/costdeck/commit/08803c9d517363b6fa14e03c3f7d9ee103467b4d))
* **finops:** price GPUs, charge them to namespaces and find idle ones ([e606244](https://github.com/migalsp/costdeck/commit/e60624475db77081382a795cea132bde23eaa306))
* **finops:** show other Cost Deck installations side by side ([bd8d3cf](https://github.com/migalsp/costdeck/commit/bd8d3cf65712545701d8d15e526f405f67a5cdbe))
* **finops:** show what LLM APIs and self-hosted models cost, by team and model ([c94fcc3](https://github.com/migalsp/costdeck/commit/c94fcc32ba9142f99a717376aad81c5872e6c086))
* **notifications:** send alerts, digests and scaling changes to Slack, Teams and email ([a69f216](https://github.com/migalsp/costdeck/commit/a69f2164cfb7e97ec5c5479a4249d8d336e48886))
* **rightsizing:** apply advice with a guard that puts it back, or open a pull request ([e6dfc87](https://github.com/migalsp/costdeck/commit/e6dfc8716b9509b7f60ea636f3d729b1cff86291))
* **scaling:** add namespaces to a schedule by label ([e8b7460](https://github.com/migalsp/costdeck/commit/e8b7460fa10a0bc6aef49becc385e50a4fa8388e))
* **scaling:** keep schedules up or down on given dates and holiday calendars ([b1d4bc8](https://github.com/migalsp/costdeck/commit/b1d4bc8fc0992042225fa8cf0e77f775d94ca55c))
* **scaling:** let idle GPUs put a namespace to sleep ([c2e48b1](https://github.com/migalsp/costdeck/commit/c2e48b141795fae5d8c6d5e53984789d05a732e5))
* **scaling:** pause Flux and Argo CD while a schedule keeps workloads down ([238d1b2](https://github.com/migalsp/costdeck/commit/238d1b23a8ced5f51a35365100f819aa4b47d27b))
* **scaling:** preview a schedule and try it in a dry run ([4458d5c](https://github.com/migalsp/costdeck/commit/4458d5c5ad09091def0feb9a581b84633f6e7a80))
* **scaling:** put namespaces to sleep when nobody uses them ([c44ccd7](https://github.com/migalsp/costdeck/commit/c44ccd7ee693734021f2099c89bbc2159fe9e14d))
* **scaling:** scale Argo Rollouts with their namespace ([b01524d](https://github.com/migalsp/costdeck/commit/b01524da96f7e1e656642d6f79a5c8469c731286))
* **scaling:** show a sleep page while a namespace is down ([d0e31dd](https://github.com/migalsp/costdeck/commit/d0e31dd34cbc948df35c876ee9fde58409192a75))
* **scaling:** stop operator-run databases and services with their namespace ([bbfc5fc](https://github.com/migalsp/costdeck/commit/bbfc5fc05410d8e1b5222aa98f0a95bd87cd9c22))
* **scaling:** suggest a schedule from a week of usage ([17b3b34](https://github.com/migalsp/costdeck/commit/17b3b344e13c25fe0c44c30863dc93136058efce))
* **settings:** set the dashboard address used by links that lead into Cost Deck ([d0e31dd](https://github.com/migalsp/costdeck/commit/d0e31dd34cbc948df35c876ee9fde58409192a75))
* **ui:** add a dark theme ([f71970d](https://github.com/migalsp/costdeck/commit/f71970dabd26a7ce5af92e210a3880cb18ece814))
* **ui:** give every page its own address ([89273b0](https://github.com/migalsp/costdeck/commit/89273b0febe614abe6ffed5e4f686aec3b710010))
* **ui:** report the outcome of actions in toasts ([3240475](https://github.com/migalsp/costdeck/commit/32404751b48cfe3f13d7b6021607e3098462ad05))


### Bug Fixes

* **deps:** update Vite to 8.3.3 for the launch-editor advisories ([ca43c76](https://github.com/migalsp/costdeck/commit/ca43c7643c987a2428b5c3bf7434416ce05aab3f))
* **finops:** keep changes made to the cost history between samples ([2d3b956](https://github.com/migalsp/costdeck/commit/2d3b9565b5f7803815bfa343127c7be991a6c9bd))
* **insights:** confirm reverting an earlier optimization inline instead of in a browser dialog ([3240475](https://github.com/migalsp/costdeck/commit/32404751b48cfe3f13d7b6021607e3098462ad05))
* **pricing:** a GPU node no longer inflates the CPU and memory rates of the whole cluster ([e606244](https://github.com/migalsp/costdeck/commit/e60624475db77081382a795cea132bde23eaa306))
* **reports:** say so when deleting a report fails instead of doing nothing ([3240475](https://github.com/migalsp/costdeck/commit/32404751b48cfe3f13d7b6021607e3098462ad05))
* **reports:** say the digest and budget alerts go to every channel under Settings → Notifications ([0228729](https://github.com/migalsp/costdeck/commit/02287295028881e67629286f41846ccc962695ac))
* **reports:** show a failed post of the digest to Webex as an error instead of in the grey text of the success message ([3240475](https://github.com/migalsp/costdeck/commit/32404751b48cfe3f13d7b6021607e3098462ad05))
* **settings:** ask before leaving unsaved settings ([873895b](https://github.com/migalsp/costdeck/commit/873895b697aa9ce35f267dec26aed0f327c140bb))
* **settings:** give every settings card the same named switch, fields and connection test ([0228729](https://github.com/migalsp/costdeck/commit/02287295028881e67629286f41846ccc962695ac))
* **tokens:** ask before revoking an API token, which took a single click and cannot be undone ([3240475](https://github.com/migalsp/costdeck/commit/32404751b48cfe3f13d7b6021607e3098462ad05))
* **ui:** draw a schedule whose days are all outside the week as always on ([34701ea](https://github.com/migalsp/costdeck/commit/34701ea0e2f17185890df62cb26fb6d3240cf543))
* **ui:** keep every page reachable from the sidebar on a laptop screen ([0228729](https://github.com/migalsp/costdeck/commit/02287295028881e67629286f41846ccc962695ac))
* **ui:** keep status badges on one line ([58825af](https://github.com/migalsp/costdeck/commit/58825af5336573c409370f5868c63d8d08d69f9a))
* **ui:** show why data could not load instead of a broken page ([24b56f1](https://github.com/migalsp/costdeck/commit/24b56f19ccf57065ab94e477b04a7be49011b646))

## [1.5.7](https://github.com/migalsp/costdeck/compare/v1.5.6...v1.5.7) (2026-10-10)


### Bug Fixes

* **deps:** update golang.org/x/net to v0.60.0 and Go to 1.26.9 for the HTTP/2 vulnerabilities ([81d1b65](https://github.com/migalsp/costdeck/commit/81d1b657fbcfb1c64e48464aa9652a7b10e61b57))
* **notify:** announce a group or schedule only when it settles in a new state ([41d535a](https://github.com/migalsp/costdeck/commit/41d535a5d61b4db2f5a3a32484a65dbfbc77b291))

## [1.5.6](https://github.com/migalsp/costdeck/compare/v1.5.5...v1.5.6) (2026-10-09)


### Bug Fixes

* **ui:** make the entrance animations work without tailwindcss-animate ([a2d2e02](https://github.com/migalsp/costdeck/commit/a2d2e021c9ce05358ca21021d39539bad4c7bdbc))

## [1.5.5](https://github.com/migalsp/costdeck/compare/v1.5.4...v1.5.5) (2026-10-07)


### Bug Fixes

* **insights:** chart namespace usage over the VictoriaMetrics lookback window ([4076a1f](https://github.com/migalsp/costdeck/commit/4076a1f9b137ae8a0b0f12a3e67e420bf2bcb5ec))
* **scaling:** leave excluded workloads out when deciding a namespace is up or down ([842c08f](https://github.com/migalsp/costdeck/commit/842c08fc0df8cd78502e4ff5f25f2626299912ad))
* **ui:** read memory quantities in decimal units (500M, 1G) in the namespace charts, which were shown as almost zero ([4076a1f](https://github.com/migalsp/costdeck/commit/4076a1f9b137ae8a0b0f12a3e67e420bf2bcb5ec))

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
