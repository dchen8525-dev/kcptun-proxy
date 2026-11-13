'use strict';
// Launch Electron with the project as the app, after stripping
// ELECTRON_RUN_AS_NODE. If that var leaks into the environment (e.g. from a
// sibling project that runs its tunnel with ELECTRON_RUN_AS_NODE=1), Electron
// degrades to plain Node and `require('electron')` resolves to the npm shim
// string instead of the API, so `app` is undefined and the app crashes on
// startup. Removing it here keeps `npm start` robust regardless of the shell.
const { spawn } = require('node:child_process');

delete process.env.ELECTRON_RUN_AS_NODE;

// Under plain Node, `require('electron')` returns the absolute path to the
// Electron executable (the npm shim's getElectronPath output).
const electronPath = require('electron');

const child = spawn(electronPath, ['.'], { stdio: 'inherit', windowsHide: false });
child.on('exit', (code) => process.exit(code === null ? 1 : code));
