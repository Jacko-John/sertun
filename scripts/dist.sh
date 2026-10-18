#!/bin/sh
# Build six pure-Go targets and package only runtime files and examples.
set -eu

release_version=${1:?usage: sh scripts/dist.sh VERSION}
case "$release_version" in
    ''|.|..|*[!A-Za-z0-9.+-]*)
        echo "invalid release version" >&2
        exit 1
        ;;
esac

cd "$(dirname "$0")/.."
release_root=$(pwd)
release_name=${release_version#v}
release_output="$release_root/dist/$release_version"
release_stage=$(mktemp -d "${TMPDIR:-/tmp}/sertun-dist.XXXXXX")
release_bundle="$release_stage/packages"
trap 'rm -rf "$release_stage"' EXIT
trap 'exit 1' HUP INT TERM
mkdir -p "$release_bundle" "$release_output"

# Keep build paths and checkout metadata out of distributed executables.
for release_os in linux windows darwin; do
    for release_arch in amd64 arm64; do
        release_asset="sertun_${release_name}_${release_os}_${release_arch}"
        release_target="$release_stage/$release_asset"
        release_binary=sertun
        if [ "$release_os" = windows ]; then release_binary=sertun.exe; fi
        mkdir -p "$release_target/examples"
        CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" \
            go build -trimpath -buildvcs=false \
            -ldflags "-s -w -X main.version=$release_version" \
            -o "$release_target/$release_binary" ./cmd/sertun
        cp examples/server.toml examples/client.toml "$release_target/examples/"
        cp THIRD_PARTY_NOTICES.txt "$release_target/"
        cat > "$release_target/README.txt" <<EOF
sertun $release_version
TCP reverse port mapping over direct TCP or an HTTP CONNECT proxy.

1. Copy examples/server.toml and examples/client.toml to the deployment hosts.
2. Generate a token with: $release_binary token
3. Set the same client ID, token and service names on both hosts.
4. Start the server: $release_binary server -c server.toml
5. Start the client: $release_binary client -c client.toml

Run the executable using its local path or install it on PATH.
Transport encryption is provided by the outer proxy or trusted network.
Documentation: https://github.com/Jacko-John/sertun
EOF
        if [ "$release_os" = windows ]; then
            (cd "$release_target" && zip -q -X "$release_bundle/$release_asset.zip" \
                "$release_binary" examples/server.toml examples/client.toml README.txt THIRD_PARTY_NOTICES.txt)
        else
            # Normalize ownership and omit macOS metadata from public archives.
            case "$(tar --version)" in
                *bsdtar*)
                    COPYFILE_DISABLE=1 tar --uid 0 --gid 0 --uname root --gname root \
                        -czf "$release_bundle/$release_asset.tar.gz" -C "$release_target" \
                        "$release_binary" examples/server.toml examples/client.toml README.txt THIRD_PARTY_NOTICES.txt
                    ;;
                *)
                    tar --owner=0 --group=0 --numeric-owner \
                        -czf "$release_bundle/$release_asset.tar.gz" -C "$release_target" \
                        "$release_binary" examples/server.toml examples/client.toml README.txt THIRD_PARTY_NOTICES.txt
                    ;;
            esac
        fi
    done
done

# Checksums contain relative archive names and no local filesystem paths.
(
    cd "$release_bundle"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum ./*.tar.gz ./*.zip
    else
        shasum -a 256 ./*.tar.gz ./*.zip
    fi
) > "$release_bundle/SHA256SUMS"
cp "$release_bundle/"* "$release_output/"
echo "Release files: dist/$release_version"
