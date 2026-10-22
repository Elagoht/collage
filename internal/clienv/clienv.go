// Package clienv names what the collage binary tells a program it runs through
// the environment, so the CLI and the framework read the same name.
package clienv

// Command is the environment variable the collage binary sets when it runs a
// program only to ask it something — "collage-inspect" for collage inspect and
// collage routes, "collage-check" for collage check — to the word it asks with.
//
// A program that never hands that word to collage.DispatchCommands starts
// serving instead, and the CLI would wait on it forever; App.ListenAndServe
// refuses to serve while it is set, so the run fails with the reason.
const Command = "COLLAGE_CLI_COMMAND"
