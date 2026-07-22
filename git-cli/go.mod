module github.com/aisphereio/aisphere-git-cli

go 1.26.4

require (
	github.com/spf13/cobra v1.10.2
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
)

replace (
	github.com/aisphereio/aisphere-iam => ../aisphere-iam
	github.com/aisphereio/kernel => ../kernel
)
