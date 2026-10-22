#!/bin/sh
# Source before rendering; these checks do not create or manage resources.
check_job_name() {
  case "$1" in
    ''|*[!a-z0-9-]*|-*|*-)
      printf '%s\n' 'Job name must be a DNS label: lowercase letters, digits and hyphens, starting and ending with a letter or digit' >&2
      return 2 ;;
  esac
  if [ "${#1}" -gt 63 ]; then
    printf '%s\n' 'Job name must not exceed 63 characters (including prefixes and worker suffixes)' >&2
    return 2
  fi
}
