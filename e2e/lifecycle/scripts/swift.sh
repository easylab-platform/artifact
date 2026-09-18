#!/bin/sh
# swift lifecycle: swift package-registry publish / resolve public+private /
# upgrade / delete (DELETE on the release path).
set -e
cat /etc/easysidecar/ca.crt >> /etc/ssl/certs/ca-certificates.crt
export HOME="/tmp/lc-home-${STAGE}"
# SwiftPM's security fingerprint DB pins the first checksum it saw for a
# package; start each run clean so a re-published version is not rejected.
rm -rf /root/.swiftpm/security /root/.cache/org.swift.swiftpm/registries
mkdir -p /root/.swiftpm/configuration
swift package-registry set --global https://api.spm.swift.org >/dev/null 2>&1

make_pkg() { # $1 = version
  rm -rf "$WORK/pkg"
  mkdir -p "$WORK/pkg/Sources/LcProbe"
  cd "$WORK/pkg"
  cat > Package.swift <<X
// swift-tools-version:5.9
import PackageDescription
let package = Package(
  name: "LcProbe",
  products: [.library(name: "LcProbe", targets: ["LcProbe"])],
  targets: [.target(name: "LcProbe")]
)
X
  cat > Sources/LcProbe/LcProbe.swift <<X
public enum LcProbe {
  public static let version = "$1"
}
X
}

publish_pkg() { # $1 = version
  make_pkg "$1"
  mkdir -p "$WORK/scratch"
  swift package-registry publish lc.probe "$1" --scratch-directory "$WORK/scratch" 2>&1 | tail -1
}

use_app() { # $1 = version constraint, $2 = expected
  rm -rf "$WORK/use"
  mkdir -p "$WORK/use/Sources/Use"
  cd "$WORK/use"
  cat > Package.swift <<X
// swift-tools-version:5.9
import PackageDescription
let package = Package(
  name: "Use",
  dependencies: [.package(id: "lc.probe", from: "$1")],
  targets: [.executableTarget(name: "Use", dependencies: [.product(name: "LcProbe", package: "lc.probe")])]
)
X
  cat > Sources/Use/main.swift <<X
import LcProbe
if LcProbe.version != "$2" { fatalError("bad version " + LcProbe.version) }
print("swift: lc.probe " + LcProbe.version)
X
  swift package resolve >/dev/null 2>&1
  swift run 2>&1 | tail -1
}

case "$STAGE" in
publish)
  publish_pkg "$V1"
  echo "swift: published lc.probe@$V1"
  ;;
public)
  # SwiftPM has no public registry upstream; the pull-through half of this
  # protocol is exercised by the SCM bridge in scripts/swift.sh of the pull
  # matrix. Here assert the registry serves an anonymous listing.
  code="$(curl -s -o /dev/null -w '%{http_code}' "https://api.spm.swift.org/lc/probe")"
  [ "$code" = "200" ] || { echo "swift: releases rc=$code"; exit 1; }
  echo "swift: registry reachable (no public upstream)"
  ;;
private)
  use_app "$V1" "$V1"
  ;;
upgrade)
  publish_pkg "$V2"
  use_app "$V2" "$V2"
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "https://api.spm.swift.org/lc/probe/$V2")"
  [ "$code" = "200" ] || [ "$code" = "204" ] || { echo "swift: delete rc=$code"; exit 1; }
  rm -rf "$WORK/use" /root/.swiftpm/security
  rel="$(curl -s "https://api.spm.swift.org/lc/probe")"
  echo "$rel" | grep -q ""$V2"" && { echo "swift: $V2 still listed after delete"; exit 1; }
  echo "$rel" | grep -q ""$V1"" || { echo "swift: $V1 vanished too"; exit 1; }
  echo "swift: deleted lc.probe@$V2 ($V1 survives)"
  ;;
esac
