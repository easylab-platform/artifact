#!/bin/sh
set -e
gem install --no-document rake
# Run assertion: execute the installed gem.
ruby -e 'require "rake"; puts "ruby + rake: #{Rake::VERSION}"'
