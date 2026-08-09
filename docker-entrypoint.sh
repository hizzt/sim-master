#!/bin/sh
set -eu

bundle=/usr/local/lib/sim-master
install -m 0755 "$bundle/simadmin" /opt/simadmin/simadmin
install -m 0755 "$bundle/simadmin-vowifi-helper" /opt/simadmin/simadmin-vowifi-helper
install -m 0644 "$bundle/LICENSE" /opt/simadmin/LICENSE
install -m 0644 "$bundle/THIRD_PARTY_NOTICES.md" /opt/simadmin/THIRD_PARTY_NOTICES.md
if [ ! -d /opt/simadmin/www ]; then
  cp -a "$bundle/www" /opt/simadmin/www
fi

exec "$@"
