#!/bin/sh
# CPAN (Perl): a plain HTTP tree. Fetch the package index and a distribution
# through the spoofed proxy (cpan.metacpan.org -> artifact-cpan), which is what
# cpanm does before it builds a module.
set -e
CA=/etc/easyproxy/ca.crt
W="wget --ca-certificate=$CA"
wget --ca-certificate=$CA --header='Range: bytes=0-131071' -qO /tmp/02packages "https://cpan.metacpan.org/modules/02packages.details.txt.gz"
[ -s /tmp/02packages ] || { echo "cpan: empty index"; exit 1; }
$W -qO /tmp/DBI.tar.gz "https://cpan.metacpan.org/authors/id/T/TI/TIMB/DBI-1.643.tar.gz"
tar -tzf /tmp/DBI.tar.gz >/dev/null || { echo "cpan: bad tarball"; exit 1; }
echo "cpan: 02packages + DBI-1.643.tar.gz ok"
