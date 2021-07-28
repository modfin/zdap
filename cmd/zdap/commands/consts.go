package commands


const BashCompletion =`
## PART 1: Write the following to eg. /etc/bash_completion.d/zdap

#! /bin/bash

: ${PROG:=$(basename ${BASH_SOURCE})}

_cli_bash_autocomplete() {
  if [[ "${COMP_WORDS[0]}" != "source" ]]; then
    local cur opts base
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    if [[ "$cur" == "-"* ]]; then
      opts=$( ${COMP_WORDS[@]:0:$COMP_CWORD} ${cur} --generate-bash-completion )
    else
      opts=$( ${COMP_WORDS[@]:0:$COMP_CWORD} --generate-bash-completion )
    fi
    COMPREPLY=( $(compgen -W "${opts}" -- ${cur}) )
    return 0
  fi
}

complete -o bashdefault -o default -o nospace -F _cli_bash_autocomplete $PROG
unset PROG`

const ZshCompletion =`
## PART 1: Write the following to eg. ~/.zdap/zsh_autocomplete

#compdef $PROG

_cli_zsh_autocomplete() {

  local -a opts
  local cur
  cur=${words[-1]}
  if [[ "$cur" == "-"* ]]; then
    opts=("${(@f)$(_CLI_ZSH_AUTOCOMPLETE_HACK=1 ${words[@]:0:#words[@]-1} ${cur} --generate-bash-completion)}")
  else
    opts=("${(@f)$(_CLI_ZSH_AUTOCOMPLETE_HACK=1 ${words[@]:0:#words[@]-1} --generate-bash-completion)}")
  fi

  if [[ "${opts[1]}" != "" ]]; then
    _describe 'values' opts
  else
    _files
  fi

  return
}

compdef _cli_zsh_autocomplete $PROG


## PART 2: Write the following to eg.  ~/.zshrc

PROG=zdap
_CLI_ZSH_AUTOCOMPLETE_HACK=1
source ~/.zdap/zsh_autocomplete
`

const FishCompletion = `
## Write the following to eg. ~/.config/fish/completions/zdap.fish

function __fish_get_zdap_command
  set cmd (commandline -opc)
  eval $cmd --generate-bash-completion
end
complete -f -c zdap -a "(__fish_get_zdap_command)"
`