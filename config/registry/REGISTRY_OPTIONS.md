# Registry options for e2e (push tests)

We need an in-cluster OCI/Docker v2 registry with **credentials** so we can test:
- Push from kaniko using a secret
- Pull with credentials (verify image content)
- Pull/push without credentials must fail

Options considered:

| Option | Pros | Cons |
|--------|------|------|
| **Docker Distribution (registry:2)** | Reference implementation, widely used, simple. Native htpasswd auth via env (REGISTRY_AUTH_HTPASSWD_*). | None for e2e. |
| **Zot** | OCI-native, single binary, built-in htpasswd in config. | Config format less familiar; we'd need to maintain a Zot config file. |
| **Harbor** | Full-featured. | Heavy (DB, Redis, etc.); overkill for e2e. |

**Choice: Docker Distribution (registry:2)** with htpasswd auth. It is the standard, works with Kaniko and any Docker client, and auth is configured with environment variables and a mounted htpasswd file.

Credentials used by this kustomization (for e2e only):
- User: `e2epush`
- Password: `e2epushpass`

The e2e suite creates a Secret of type `kubernetes.io/dockerconfigjson` with these credentials for the registry URL so that kaniko and verification pods can push/pull.
