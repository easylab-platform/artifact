#!/bin/sh
set -e
mkdir -p /w/src && cd /w
printf '[package]\nname="t"\nversion="0.1.0"\nedition="2021"\n\n[dependencies]\nanyhow="1"\n' > Cargo.toml
cat > src/main.rs <<'X'
fn main() {
    let e = anyhow::anyhow!("boom");
    println!("cargo + anyhow: {}", e);
}
X
# Compile/run assertion (fetches deps + links a binary).
cargo run --quiet
