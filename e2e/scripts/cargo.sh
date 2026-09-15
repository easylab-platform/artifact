#!/bin/sh
set -e
mkdir -p /w/src && cd /w
printf '[package]\nname="t"\nversion="0.1.0"\nedition="2021"\n\n[dependencies]\nanyhow="1"\n' > Cargo.toml
echo 'fn main(){}' > src/main.rs
cargo fetch
