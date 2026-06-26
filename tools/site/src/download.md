---
layout: doc
---

# Download Kitsoki

Prebuilt `kitsoki` binaries are published on GitHub Releases for the normal local-use platforms:

| Platform | Architecture | Asset |
|---|---:|---|
| macOS | Apple Silicon | `kitsoki_<version>_darwin_arm64.tar.gz` |
| macOS | Intel | `kitsoki_<version>_darwin_amd64.tar.gz` |
| Linux | x86_64 | `kitsoki_<version>_linux_amd64.tar.gz` |
| Linux | ARM64 | `kitsoki_<version>_linux_arm64.tar.gz` |
| Windows | x86_64 | `kitsoki_<version>_windows_amd64.zip` |

[Open the latest release](https://github.com/bsacrobatix/Kitsoki/releases/latest)

## Install

Download the archive for your platform, extract it, then put `kitsoki` on your `PATH`.

```sh
kitsoki version
```

The release also includes `checksums.txt`. Verify the archive before installing when possible:

```sh
sha256sum -c checksums.txt
```

## Build From Source

If you want to build from the repository instead:

```sh
make setup
make install
```

See [Getting Started](./guide/getting-started.html) for the full local setup path.
