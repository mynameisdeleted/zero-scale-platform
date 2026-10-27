# Deployment Examples

App configs for `zero-scale-ctl apply`. Each `.conf` describes one app; `apply` provisions its
database and secrets, uploads the binary to `/opt/apps`, registers it with the gateway and routes its hosts.

```bash
zero-scale-ctl apply -f examples/mdm-example.conf
zero-scale-ctl apply -f examples/mdm-example.conf -n qa   # into an environment
```

`binary_path`, `config_file` and `config_files` are resolved relative to the `.conf` file, so either put the
binary next to the config or use an absolute path.

## Keys

| Key | Meaning |
|---|---|
| `hostname` | Host the app serves; repeat it, or use a `hostname:` list, for more hosts. The first is canonical and names the app (`mdm.ffxm.cc` → `mdm`). `${ENV_SUFFIX}` and `${ENV}` expand per environment. |
| `binary_path` | Local binary to upload. Required for apps. |
| `args` | Arguments passed to the binary (e.g. `serve`). |
| `init_cmd` | Command run once in the gateway pod during `apply` (migrations). `${BINARY}` and `${HOSTNAME}` expand. |
| `user`, `dynamic_user` | Run as a fixed user, or (default) a transient `DynamicUser`. |
| `idle_timeout` | Stop the app after this much inactivity (`60s`, `5m`). |
| `scale_to_zero` | `false` keeps the app running. |
| `volumes` | List of `name:/path`, `name:/path:ro` or `name:/path:tmp`. |
| `config_files` | Files templated with `${VAR}` from env and secrets and uploaded next to the binary. Names must start with the binary or app name (`mdm-config.yaml`). |
| `env:` | Block of `KEY: value` environment variables. `${PORT}` and `${HOST}` expand. |
| `secrets:` | List of `- KEY: source`, or `- KEY` to generate a random value. |
| `database:` | `engine: postgres\|mysql` plus `env_address`, `env_host`, `env_port`, `env_database`, `env_user`, `env_password` naming the variables the app reads. |
| `redis:` | `env_address`, `env_host`, `env_port`. |
| `gateway:` | `oauth_setup`, `oauth_url`, `oauth_group` to put the app behind the shared oauth2-proxy. |
| `launcher: \|` | Indented shell script that replaces the generated launcher. |
| `target_host` | Makes the file an edge redirect: `hostname` 301s to `target_host`, with no binary. |

## Examples

| File | Shows |
|---|---|
| [`mdm-example.conf`](mdm-example.conf) | Fixed user, custom idle timeout, OAuth behind a Gitea org, secrets shared from another app. |
