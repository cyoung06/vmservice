# vmservice

A self-service portal for [KubeVirt](https://kubevirt.io) VMs. Members sign in with OpenID Connect
and manage their own machines: create one from an image, start, stop, restart, delete, and open its
serial console or VNC display in the browser.

Members never get Kubernetes credentials. vmservice is the only writer in one namespace, and every
machine there is labeled with its owner: a stable id derived from the issuer and the ID token's
`sub`, so a username that is freed and signed up again doesn't inherit anything. The username is
kept as an annotation (`vmservice/owner-name`) for admins. Every request is checked against that label, and a
machine that isn't yours looks the same as one that doesn't exist.

It is a Next.js static export served by a small Go binary, on its own origin.

## How it works

- **Sign-in is OpenID Connect**: the authorization code flow with PKCE. The ID token must list
  `-group` in its `-groups-claim`, which is checked once at sign-in. The session is an encrypted
  cookie (AES-256-GCM) holding the user's stable id, the username and an expiry, and nothing is
  stored on the server.
  Removing someone from the group takes effect when their cookie expires (`-session-lifetime`).
- **A machine** is a KubeVirt `VirtualMachine`:
  - Its root disk is a `dataVolumeTemplates` clone of an image PVC in the same namespace (CDI).
  - It boots with cloud-init from a Secret the VM owns. The cloud-init sets the hostname, sets the
    image's default user's password (as a SHA-512 crypt hash; the plaintext isn't kept), and turns
    off SSH password logins.
  - It runs on the pod network with masquerade.
  - Its run strategy is `Manual`: it runs when started from the portal, and a shutdown from inside
    the guest leaves it stopped.
  - Deleting the VM deletes its disk and Secret with it.
- **Data lives in two places.** The machine itself is its KubeVirt object (what it is and does). Whose
  it is lives in Postgres, next to the members and their SSH keys: `machines(id, owner, …)`,
  `members`, `ssh_keys`. Create writes the row first, then the VM; delete removes the VM, and the VM
  watch removes the row once it's gone. A crash leaves at most a row without a VM (shown as failed;
  the member deletes it), never a VM nobody owns.
- **Limits** are per member (machines, vCPUs, memory, disk) and checked in one SERIALIZABLE
  transaction with the insert, so they hold however many requests arrive at once.
- **Open streams** (live views, consoles and bastion sessions) are capped per member (`-max-streams`).
- **SSH keys** are registered in the portal (one member per key) and go into each machine created
  afterwards (cloud-init `ssh_authorized_keys`).
- **The SSH bastion** (optional, `-ssh-listen`): `ssh -J host:port user@vm-xxxxxx`. The key says who
  you are; the only thing allowed is forwarding to port 22 of a running machine of yours.
- **Live state** comes from an informer on the namespace's VMs and DataVolumes, and reaches the
  browser as server-sent events (`/api/vms/events`). There is no polling.
- **Consoles**: the browser's websocket is passed through to KubeVirt's VMI `console` and `vnc`
  subresources. vmservice checks the session and the owner, then opens the upstream connection with
  its service account's token and copies bytes both ways. Consoles close when the session expires.
- **Requests are checked for the same origin.** Writes need `X-Requested-With` and a same-origin
  `Origin`. Websockets need an `Origin` equal to `-origin`.

The HTTP API is in [docs/api.md](docs/api.md).

## Running it

```sh
vmservice -namespace vms -catalog /etc/vmservice/catalog.yaml -origin https://vm.example.org \
  -oidc-issuer https://sso.example.org/realms/main -oidc-client-id vmservice \
  -oidc-client-secret-file /etc/vmservice/oidc/client-secret -group vm-access \
  -session-key-file /etc/vmservice/session/key -login-provider Keycloak
```

| Flag | Default | |
| --- | --- | --- |
| `-listen` | `:8080` | plain HTTP; TLS terminates in front |
| `-namespace` | (required) | the machines' and images' namespace |
| `-catalog` | (required) | images, sizes, disk range, limits ([example](deploy/catalog.example.yaml)) |
| `-origin` | (required) | the public origin; the redirect URI is `<origin>/auth/callback` |
| `-oidc-issuer` | (required) | the OpenID Connect issuer |
| `-oidc-client-id` | (required) | |
| `-oidc-client-secret-file` | (required) | a file holding the client secret |
| `-group` | | the group required to sign in; empty: anyone the provider signs in |
| `-groups-claim` | `groups` | a list of group names; a leading `/` is ignored |
| `-username-claim` | `preferred_username` | shown to the user and kept as an annotation; ownership uses `sub` |
| `-session-key-file` | (required) | 32 bytes, raw or base64 |
| `-database-url-file` | (required) | a file holding the Postgres URL; the schema is migrated at start |
| `-session-lifetime` | `168h` | how long a sign-in lasts |
| `-max-streams` | `8` | open consoles, live views and bastion sessions per member, together |
| `-ssh-listen` | | the bastion's listen address, e.g. `:2222`; empty: no bastion |
| `-ssh-host-key-file` | | the bastion's host key (OpenSSH private key file) |
| `-ssh-public` | `-origin`'s host, `-ssh-listen`'s port | the bastion's `host:port` as members reach it (shown in the UI) |
| `-title` | `VMService` | sidebar and tab title |
| `-login-provider` | | name on the button ("Sign in with …") |

Serves `/` (pages), `/config.json`, `/api/…`, `/auth/…` and `/healthz`.

## Cluster requirements

- **Postgres** (any recent version) for members, rights and SSH keys.
- **KubeVirt and CDI.** The images are PVCs in `-namespace`, for example DataVolumes imported from
  the distributions' cloud images. A storage class with CSI snapshots lets CDI clone them without
  copying.
- **Images need cloud-init 22.2 or later.** The password is set with `chpasswd.users`, which
  older cloud-init ignores, leaving no usable console login.
- **Credentials.** vmservice runs in the cluster with its service account's token. Outside the
  cluster, the consoles only work with a kubeconfig that holds a bearer token (or token file) for an
  https API server, not with exec plugins.
- **Permissions.** The service account needs the namespaced Role in
  [deploy/rbac.yaml](deploy/rbac.yaml) and nothing outside the namespace. It can create Secrets but
  can't read them.
- **The OIDC client** needs the redirect URI `<origin>/auth/callback` and a groups claim in the ID
  token. For Keycloak, that's a group membership mapper with full paths off.
- **Network policy.** The VMs are pods labeled `kubevirt.io=virt-launcher`. What they may reach is
  up to the cluster's network policy, and vmservice adds none.

## Developing

```sh
cd web && npm install && npm run dev      # the UI on :3000, forwarding /api, /auth and /config.json to VMSERVICE_DEV (default http://127.0.0.1:8080)
cd web && npm run embed                   # build the export into internal/web/ui
go test ./internal/...                    # VMSERVICE_TEST_DATABASE_URL=postgres://… also runs the store tests on a scratch Postgres
```

## License

MIT
