// Independent lifetime fence if the host disappears. Tool code is untrusted, so
// this is defense in depth; the host deadline/kill and durable claim own recovery.
const deadline = Number(process.argv[2]);
const remaining = deadline * 1000 - Date.now();
if (!Number.isSafeInteger(deadline) || remaining <= 0 || remaining > 3600000) process.exit(1);
setTimeout(() => process.exit(124), remaining);
