#!/bin/sh
# cargo lifecycle: cargo publish / build public+private / upgrade / yank
set -e
export CARGO_HOME="$HOME/.cargo" CARGO_NET_RETRY=5
mkdir -p "$CARGO_HOME"
cat > "$CARGO_HOME/config.toml" <<'EOF'
[registries.easylab]
index = "sparse+https://index.crates.io/"
EOF

publish_crate() { # $1 = version
  rm -rf "$WORK/crate"
  mkdir -p "$WORK/crate/src"
  cat > "$WORK/crate/Cargo.toml" <<EOF
[package]
name = "lc-probe-${SUFFIX}"
version = "$1"
edition = "2021"
license = "MIT"
description = "lc probe"
EOF
  cat > "$WORK/crate/src/lib.rs" <<EOF
pub const VERSION: &str = "$1";
EOF
  cd "$WORK/crate"
  cargo publish --registry easylab --token lc-token --allow-dirty --quiet
}

case "$STAGE" in
publish)
  publish_crate "$V1"
  echo "cargo: published lc-probe-${SUFFIX}@$V1"
  ;;
public)
  mkdir -p "$WORK/app/src" && cd "$WORK/app"
  printf '[package]\nname="t"\nversion="0.1.0"\nedition="2021"\n\n[dependencies]\nanyhow="1"\n' > Cargo.toml
  printf 'fn main() { println!("cargo+anyhow: {}", anyhow::anyhow!("boom")); }\n' > src/main.rs
  cargo run --quiet
  ;;
private)
  mkdir -p "$WORK/app/src" && cd "$WORK/app"
  cat > Cargo.toml <<EOF
[package]
name = "use"
version = "0.1.0"
edition = "2021"

[dependencies]
lc-probe-${SUFFIX} = "${V1%.*}"
EOF
  cat > src/main.rs <<EOF
fn main() {
  let v = lc_probe_${SUFFIX}::VERSION;
  assert_eq!(v, "$V1");
  println!("cargo: private lc-probe-${SUFFIX} {{v}}");
}
EOF
  cargo run --quiet
  ;;
upgrade)
  publish_crate "$V2"
  mkdir -p "$WORK/app/src" && cd "$WORK/app"
  cat > Cargo.toml <<EOF
[package]
name = "use"
version = "0.1.0"
edition = "2021"

[dependencies]
lc-probe-${SUFFIX} = "=${V2}"
EOF
  cat > src/main.rs <<EOF
fn main() {
  assert_eq!(lc_probe_${SUFFIX}::VERSION, "$V2");
  println!("cargo: upgraded to $V2");
}
EOF
  cargo run --quiet
  # Both versions present in the sparse index.
  idx="$(wget -qO- "https://index.crates.io/lc/-p/lc-probe-${SUFFIX}")"
  echo "$idx" | grep -q "\"vers\":\"$V1\""
  echo "$idx" | grep -q "\"vers\":\"$V2\""
  ;;
delete)
  cargo yank --registry easylab --token lc-token --version "$V1" "lc-probe-${SUFFIX}" >/dev/null
  idx="$(wget -qO- "https://index.crates.io/lc/-p/lc-probe-${SUFFIX}")"
  echo "$idx" | grep "\"vers\":\"$V1\"" | grep -q '"yanked":true' \
    || { echo "cargo: yank not recorded in index"; exit 1; }
  echo "cargo: yanked lc-probe-${SUFFIX}@$V1"
  ;;
esac
