# Bootstrap chain release artifacts into fish_function_path.
# Sourced automatically when installed to /etc/fish/conf.d/ or ~/.config/fish/conf.d/.
#
# Uses set -gx in the fallback branch (not set -U) so this file is safe
# in read-only container environments where fish_variables cannot be written.

if set -q CHAIN_INSTALL_PREFIX
    set -p fish_function_path $CHAIN_INSTALL_PREFIX/fish/functions
else
    set -gx CHAIN_INSTALL_PREFIX /usr/local/share/chaintools
    set -p fish_function_path /usr/local/share/chaintools/fish/functions
end
