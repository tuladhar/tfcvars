# tfcvars

Small CLI to **create workspace variables** in [Terraform Cloud](https://developer.hashicorp.com/terraform/cloud-docs) or **HCP Terraform** from a YAML file. If a variable (same **key** and **category**) already exists, it is **skipped**—safe to re-run.

This is **not** HashiCorp software; it only uses the public HTTP API.

## Requirements

- Go **1.22+** (to build from source)
- An API token with permission to read/write variables on the target workspaces ([tokens](https://developer.hashicorp.com/terraform/cloud-docs/users-teams-organizations/api-tokens))

## Install

From a clone of this repository:

```bash
cd terraform/tfcvars
go build -o tfcvars .
```

Add the binary to your `PATH`, or from the same directory run `go install .` (installs to `$(go env GOPATH)/bin`).

If this module path matches where you host the repo, you can also run:

```bash
go install github.com/covergo/sre-helper/terraform/tfcvars@latest
```

Change that path if you fork or publish the tool under another module name.

## Usage

```bash
tfcvars -config-file config.yaml
# or
tfcvars -f config.yaml
```

- **`NO_COLOR`** — disable ANSI colors
- **`TFC_ADDRESS`** — API host if not set in YAML (default: `https://app.terraform.io`)
- **`TFC_TOKEN`** / **`TF_TOKEN`** — used when `token` is not set in YAML

Copy **`config.example.yaml`** to **`config.yaml`**, edit workspaces and variables, and **do not commit** real tokens. See the example file for the full schema (`org`, `workspaces[].name`, `workspaces[].variables`, optional `workspaces[].org`).

## License

MIT — see [LICENSE](LICENSE).
