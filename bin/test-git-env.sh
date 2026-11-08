#!/usr/bin/env bash

for test_git_name in "${!GIT_@}"; do
  case "$test_git_name" in
    GIT_CONFIG_GLOBAL|GIT_CONFIG_SYSTEM|GIT_CONFIG_NOSYSTEM|GIT_TERMINAL_PROMPT) ;;
    *) unset "$test_git_name" ;;
  esac
done
unset test_git_name
