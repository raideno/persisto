### Release Process

```bash
# NOTE: create a new release
make tag-release TAG=v1.0.0

# NOTE: or manually
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

Releases are automatically built with GoReleaser and published to GitHub Container Registry.
