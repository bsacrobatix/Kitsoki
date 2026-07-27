# External worker-image pointer

Hosted worker pools may select their image through a deployment-controlled
file instead of a static checked-in image reference:

```yaml
remotes:
  production-workers:
    pool:
      token_env: DO_POOL_TOKEN
      image_pointer: /etc/kitsoki/worker-image.json
      image_pointer_environment: production
      size: s-4vcpu-16gb
      region: sgp1
```

`image` and `image_pointer` are mutually exclusive. Static `image` behavior is
unchanged outside pointer mode. Pointer mode has no fallback: a missing,
unsafe, malformed, or wrong-environment file prevents the lease before a
durable worker record or provider request is created.

The pointer schema is:

```json
{
  "schema": "kitsoki/worker-image-pointer/v1",
  "environment": "production",
  "generation": 17,
  "source_sha": "0123456789abcdef0123456789abcdef01234567",
  "image_id": "192837465",
  "image_digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "activated_at": "2026-07-26T13:00:00Z"
}
```

The deployment controller must write a new regular file, fsync it, rename it
over the pointer, and fsync `/etc/kitsoki`. The file and every parent directory
must be root-owned and not group- or world-writable; symlinks are rejected.
On macOS only, the operating-system `/etc` compatibility alias is normalized to
`/private/etc` before that validation, so the documented pointer spelling works
without accepting caller-controlled symlink paths. Kitsoki opens the leaf with
`O_NOFOLLOW`, validates one bounded JSON value with no unknown fields, and uses
that one open inode as the lease snapshot.

That compatibility mapping applies only to the cleaned exact `/etc` path
component. It is not general symlink resolution: `/etc/../...` and every other
symlinked parent remain subject to the ordinary rejection path.

`Pool.Acquire` resolves the pointer for every new worker. It writes the exact
generation, environment, source SHA, image ID, and digest into durable vmpool
state before asking the provider to create the VM. Pointer activation therefore
affects only later leases. Existing workers remain bound to their recorded
image, including when activation races with an acquire.

The source SHA accepts immutable 40- or 64-character lowercase Git object IDs.
The DigitalOcean image ID is a positive decimal string. The image digest is
always `sha256:` followed by 64 lowercase hexadecimal characters.
