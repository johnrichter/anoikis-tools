module github.com/johnrichter/anoikis-tools

go 1.26

require (
	github.com/bmatcuk/doublestar/v4 v4.10.0
	github.com/johnrichter/claude-shared-tooling/go/bandcheck v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/clikit v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/cost v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/docmirror v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/fsx v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/gate v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/git v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/graph v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/jsondoc v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/ledger v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/retrieve v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/roster v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/schema v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/state v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/sysops v0.0.0
	github.com/johnrichter/claude-shared-tooling/go/transcript v0.0.0
	github.com/knadh/koanf/parsers/yaml v1.1.0
	github.com/knadh/koanf/providers/confmap v1.0.0
	github.com/knadh/koanf/providers/env v1.1.0
	github.com/knadh/koanf/providers/file v1.2.1
	github.com/knadh/koanf/providers/posflag v1.0.1
	github.com/knadh/koanf/v2 v2.3.5
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	go.yaml.in/yaml/v3 v3.0.4
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/google/renameio/v2 v2.0.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gowebpki/jcs v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/johnrichter/claude-shared-tooling/go/logkit v0.0.0 // indirect
	github.com/knadh/koanf/maps v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.54.0 // indirect
)

// The claude-shared-tooling modules are ai-shared-lib sibling-repo libraries
// (../ai-shared-lib/go/*), not yet independently tagged -- this placeholder
// version plus a local replace is a monorepo-development stand-in a future
// release transaction resolves by cutting real tags and pointing these
// requires at them. A replace directive is only honored in the MAIN module's
// own go.mod, so the full transitive closure is replaced here, including
// modules this CLI never imports directly.
replace github.com/johnrichter/claude-shared-tooling/go/bandcheck => ../ai-shared-lib/go/bandcheck

replace github.com/johnrichter/claude-shared-tooling/go/clikit => ../ai-shared-lib/go/clikit

replace github.com/johnrichter/claude-shared-tooling/go/cost => ../ai-shared-lib/go/cost

replace github.com/johnrichter/claude-shared-tooling/go/docmirror => ../ai-shared-lib/go/docmirror

replace github.com/johnrichter/claude-shared-tooling/go/fsx => ../ai-shared-lib/go/fsx

replace github.com/johnrichter/claude-shared-tooling/go/gate => ../ai-shared-lib/go/gate

replace github.com/johnrichter/claude-shared-tooling/go/git => ../ai-shared-lib/go/git

replace github.com/johnrichter/claude-shared-tooling/go/graph => ../ai-shared-lib/go/graph

replace github.com/johnrichter/claude-shared-tooling/go/jsondoc => ../ai-shared-lib/go/jsondoc

replace github.com/johnrichter/claude-shared-tooling/go/ledger => ../ai-shared-lib/go/ledger

replace github.com/johnrichter/claude-shared-tooling/go/logkit => ../ai-shared-lib/go/logkit

replace github.com/johnrichter/claude-shared-tooling/go/retrieve => ../ai-shared-lib/go/retrieve

replace github.com/johnrichter/claude-shared-tooling/go/roster => ../ai-shared-lib/go/roster

replace github.com/johnrichter/claude-shared-tooling/go/schema => ../ai-shared-lib/go/schema

replace github.com/johnrichter/claude-shared-tooling/go/state => ../ai-shared-lib/go/state

replace github.com/johnrichter/claude-shared-tooling/go/sysops => ../ai-shared-lib/go/sysops

replace github.com/johnrichter/claude-shared-tooling/go/toolchain => ../ai-shared-lib/go/toolchain

replace github.com/johnrichter/claude-shared-tooling/go/transcript => ../ai-shared-lib/go/transcript
