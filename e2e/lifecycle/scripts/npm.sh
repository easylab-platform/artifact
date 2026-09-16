#!/bin/sh
# npm lifecycle: publish / install public / install private / upgrade / unpublish
set -e
# npm refuses to publish without a locally-known token; the open registry
# ignores its value.
printf '//registry.npmjs.org/:_authToken=lc-token\n' > "$HOME/.npmrc"
case "$STAGE" in
publish|upgrade)
  # A: publish $VER (upgrade publishes 2.0.0 after 1.0.0 exists).
  mkdir -p "$WORK/pub" && cd "$WORK/pub"
  cat > package.json <<EOF
{ "name": "$NAME", "version": "$VER", "description": "lc probe",
  "main": "index.js" }
EOF
  printf 'module.exports = { version: "%s", hello: () => "lc-%s" };\n' "$VER" "$VER" > index.js
  npm publish --access public --no-audit --no-fund
  [ "$STAGE" = "publish" ] && exit 0
  # B (fresh): v2 installs and runs; both versions are listed.
  cd "$WORK" && npm install --no-audit --no-fund --no-package-lock --prefix "$WORK/use" "$NAME@$V2" >/dev/null
  cd "$WORK/use"
  node -e "const m=require('$NAME'); if(m.version!=='$V2' || m.hello()!=='lc-$V2') process.exit(1)"
  npm view "$NAME" versions --json | grep -q "\"$V1\""
  npm view "$NAME" versions --json | grep -q "\"$V2\""
  echo "npm: $NAME $V1+$V2"
  ;;
public)
  cd "$WORK"
  npm install --no-audit --no-fund --no-package-lock left-pad >/dev/null
  node -e 'const p=require("left-pad"); if(p("7",3,"0")!=="007") process.exit(1)'
  echo "npm: public left-pad ok"
  ;;
private)
  cd "$WORK"
  npm install --no-audit --no-fund --no-package-lock "$NAME@$V1" >/dev/null
  node -e "const m=require('$NAME'); if(m.version!=='$V1') process.exit(1)"
  echo "npm: private $NAME@$V1 ok"
  ;;
delete)
  npm unpublish "$NAME" --force >/dev/null
  if npm view "$NAME" version >/dev/null 2>&1; then
    echo "npm: $NAME still present after unpublish"; exit 1
  fi
  echo "npm: $NAME unpublished"
  ;;
esac
