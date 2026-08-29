module github.com/draincloud/callpack/integration

go 1.25.0

// Never published: the round trip it tests only exists inside this repo, so it always
// builds against the working tree rather than a released version of either module.
replace github.com/draincloud/callpack/caller => ../caller

replace github.com/draincloud/callpack/registry => ../registry

require (
	github.com/draincloud/callpack/caller v0.0.0
	github.com/draincloud/callpack/registry v0.0.0
	github.com/hashicorp/consul/api v1.32.1
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/armon/go-metrics v0.4.1 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v1.5.0 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/hashicorp/serf v0.10.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20250305212735-054e65f0b394 // indirect
	golang.org/x/sys v0.31.0 // indirect
)
