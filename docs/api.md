# VMService HTTP API

Everything is served by the one binary on one origin. The UI is at `/`, the API is under `/api/`, and
sign-in is under `/auth/`. JSON in and out. An error is `{"detail": "<message for the user>"}` with a
4xx/5xx status.

The session is an encrypted cookie that holds the user's stable id (derived from the issuer and the
ID token's `sub`), the username and an expiry; nothing is stored on the server. Machines belong to the
stable id, so a username that is freed and signed up again doesn't inherit anything. The group is checked once, at sign-in. Every `/api/` request without a valid session gets `401`.
A write (POST, DELETE) must send `X-Requested-With: XMLHttpRequest`, and if it carries an `Origin`,
that must be the public origin. Otherwise it gets `403`.

## Sign-in

| | |
| --- | --- |
| `GET /auth/login?next=<path>` | redirects to the OIDC provider; after the callback, back to `next` (a local path; default `/`) |
| `GET /auth/callback` | the OIDC redirect URI |
| `POST /auth/logout` | clears the cookie; `204` |
| `GET /config.json` | `{"title": "VMService", "loginProvider": "Keycloak"}`: what the UI is told about the deployment |

## Session and catalog

`GET /api/session` → `200 {"username": "alice", "expires": "2026-11-19T10:00:00Z"}` or `401`.

`GET /api/catalog` →

```json
{
  "images": [{"id": "ubuntu-24.04", "name": "Ubuntu 24.04", "defaultUser": "ubuntu"}],
  "sizes": [{"id": "small", "name": "Small", "cpu": 1, "memoryMiB": 2048}],
  "disk": {"minGiB": 10, "maxGiB": 100, "defaultGiB": 20},
  "limits": {"vms": 3, "cpu": 8, "memoryMiB": 16384, "diskGiB": 200}
}
```

The limits cover every VM the caller owns, running or not. The UI adds up usage from the machine list.

## Machines

A machine:

```json
{
  "id": "vm-7k2q9x",
  "name": "my box",
  "image": "ubuntu-24.04",
  "imageName": "Ubuntu 24.04",
  "defaultUser": "ubuntu",
  "size": "small",
  "cpu": 1,
  "memoryMiB": 2048,
  "diskGiB": 20,
  "state": "Running",
  "detail": "",
  "createdAt": "2026-11-12T10:00:00Z"
}
```

`state` is one of:
- `Provisioning`: the disk is being cloned
- `Starting`
- `Running`
- `Stopping`
- `Stopped`
- `Error`
- `Deleting`

`detail` is a human-readable reason when there is one, e.g. a failure message.

| | |
| --- | --- |
| `GET /api/vms` | `{"vms": [machine, ...]}`: the caller's machines, newest first |
| `GET /api/vms/{id}` | a machine; `404` if it doesn't exist or isn't the caller's |
| `POST /api/vms` | body `{"name", "image", "size", "diskGiB", "password"}` → `201` machine. `400` for a bad field, `403` when over a limit. Creates run one at a time, so the limits hold however many arrive at once |
| `POST /api/vms/{id}/start` | `204` |
| `POST /api/vms/{id}/stop` | `204` |
| `POST /api/vms/{id}/restart` | `204` |
| `DELETE /api/vms/{id}` | `204`; the machine and its disk are deleted |

Field rules for `POST /api/vms`:
- **`name`:** 1–63 characters of any text. It's only a display name.
- **`image` and `size`:** ids from the catalog.
- **`diskGiB`:** an integer within `disk.minGiB` to `disk.maxGiB`.
- **`password`:** 8–128 characters. It's the default user's password for the console login. It's stored only as a SHA-512 crypt hash and can't be shown again.

### Live updates

`GET /api/vms/events` is a server-sent event stream of the caller's machines:
- **`snapshot`:** sent first. Data is `{"vms": [machine, ...]}`.
- **`vm`:** a machine was created or changed. Data is the machine.
- **`deleted`:** data is `{"id": "vm-7k2q9x"}`.

The stream ends when the session expires. `EventSource` reconnects by itself, and a `401` on reconnect means the user has to sign in again.

### Consoles

- **`GET /api/vms/{id}/serial`:** websocket. The serial console (ttyS0) as raw bytes in binary frames, both ways.
- **`GET /api/vms/{id}/vnc`:** websocket. The VNC (RFB) stream, for noVNC.

A member may have `-max-streams` event streams and consoles open together (default 8); one more gets
`429`.

Both must be opened with the subprotocol `plain.kubevirt.io`. They are KubeVirt's VMI `console`/`vnc`
subresources passed through as they are. Both need the machine to be `Running`; otherwise the upgrade
gets `409`. Both close when the session expires.

## SSH keys

A member's public keys: put into each machine they create from then on (cloud-init
`ssh_authorized_keys`), and accepted by the SSH bastion. A key belongs to one member only, so it
identifies them at the bastion.

A key:

```json
{"id": "3f1c…64 hex…", "fingerprint": "SHA256:Pz6…", "type": "ssh-ed25519", "label": "laptop", "createdAt": "2026-11-12T10:00:00Z"}
```

| | |
| --- | --- |
| `GET /api/keys` | `{"keys": [key, ...]}`: the caller's keys, newest first |
| `POST /api/keys` | body `{"publicKey": "ssh-ed25519 AAAA… comment", "label": "laptop"}` → `201` key. `400` for a key that isn't one OpenSSH public key line (ed25519, ECDSA, or RSA ≥ 2048 bits; no options) or a bad label, `409` when the key is already registered (by anyone), `403` at 20 keys |
| `DELETE /api/keys/{id}` | `204`; `404` if it isn't the caller's. Machines made earlier keep the key; the bastion stops accepting it |

`label`: 0–63 characters, display only. The comment on the key line is dropped; the label is shown instead (the comment is the default label when none is given).

`GET /config.json` also says how to reach the bastion: `"ssh": {"host": "cloud.example.org", "port": 222}`, or `"ssh": null` when it's off.

## SSH bastion

`ssh -J <host>:<port> <user>@<machine id>`, e.g. `ssh -J cloud.example.org:222 ubuntu@vm-7k2q9x`.
The jump host takes any username: the key says who you are. It allows nothing but forwarding to port
22 of a running machine of yours (no shell, no other ports or hosts); the machine id is the host name.
