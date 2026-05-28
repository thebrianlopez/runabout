#!/usr/bin/env fish
# postinstall.fish - Sets CHAIN_INSTALL_PREFIX as a Fish universal variable.
# Executed by Homebrew postinstall, dpkg postinst, or OCI ENTRYPOINT.
#
# System install (Homebrew, dpkg):
#   fish scripts/postinstall.fish
# User install (--user flag):
#   fish scripts/postinstall.fish --user

if contains -- --user $argv
    set -U CHAIN_INSTALL_PREFIX ~/.local/share/chaintools
else
    set -U CHAIN_INSTALL_PREFIX /usr/local/share/chaintools
end
