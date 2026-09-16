#!/bin/sh
# rubygems lifecycle: gem push / install public+private / upgrade / yank
# (yank has no first-class gem client for custom hosts; the adapter exposes
#  DELETE /api/v1/gems/<name>/yank?version=)
set -e
mkdir -p "$HOME/.gem"
printf ':rubygems_api_token: lc-token\n' > "$HOME/.gem/credentials"
chmod 0600 "$HOME/.gem/credentials"
export GEM_HOME="$HOME/gems"
export GEM_PATH="$GEM_HOME"

build_and_push() { # $1 = version
  mkdir -p "$WORK/gem/lib"
  cat > "$WORK/gem/lc-probe-${SUFFIX}.gemspec" <<EOF
Gem::Specification.new do |s|
  s.name = "lc-probe-${SUFFIX}"
  s.version = "$1"
  s.summary = "lc probe"
  s.authors = ["lc"]
  s.license = "MIT"
  s.files = ["lib/lc_probe_${SUFFIX}.rb"]
end
EOF
  printf 'module LcProbe\n  VERSION = "%s".freeze\nend\n' "$1" > "$WORK/gem/lib/lc_probe_${SUFFIX}.rb"
  cd "$WORK/gem"
  gem build "lc-probe-${SUFFIX}.gemspec" >/dev/null
  gem push "lc-probe-${SUFFIX}-$1.gem" --host https://rubygems.org -k rubygems_api_token >/dev/null
}

case "$STAGE" in
publish)
  build_and_push "$V1"
  echo "rubygems: pushed lc-probe-${SUFFIX}@$V1"
  ;;
public)
  gem install thor -N >/dev/null
  ruby -e 'require "thor"; Thor::Shell::Color.new.say("rubygems: public thor ok")'
  ;;
private)
  gem install "lc-probe-${SUFFIX}" -v "$V1" -N >/dev/null
  ruby -e "require 'lc_probe_${SUFFIX}'; abort 'bad version' unless LcProbe::VERSION == '${V1}'"
  echo "rubygems: private lc-probe-${SUFFIX}@$V1 ok"
  ;;
upgrade)
  build_and_push "$V2"
  gem install "lc-probe-${SUFFIX}" -v "$V2" -N >/dev/null
  ruby -e "require 'lc_probe_${SUFFIX}'; abort 'bad version' unless LcProbe::VERSION == '${V2}'"
  gem list -r -a -e "lc-probe-${SUFFIX}" 2>/dev/null | grep -q "$V1"
  echo "rubygems: upgraded to $V2, $V1 still listed"
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
    "https://rubygems.org/api/v1/gems/lc-probe-${SUFFIX}/yank?version=${V2}")"
  [ "$code" = "200" ] || { echo "rubygems: yank rc=$code"; exit 1; }
  if gem install "lc-probe-${SUFFIX}" -v "$V2" -N >/dev/null 2>&1; then
    echo "rubygems: $V2 still installable after yank"; exit 1
  fi
  echo "rubygems: yanked lc-probe-${SUFFIX}@$V2"
  ;;
esac
