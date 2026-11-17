// Build the public landing page while the hosted dashboard is not deployed.
// Go embeds website/index.html for the full application; this static build
// points calls to action at self-hosting docs instead of publishing broken /app links.
import {readFile,writeFile,mkdir,copyFile} from 'node:fs/promises';
import {resolve} from 'node:path';
const destination=process.argv[2];if(!destination)throw Error('Supply an output directory outside website/.');
const output=resolve(destination);if(output===resolve('website'))throw Error('Use a separate output directory.');
let html=await readFile('website/index.html','utf8');
html=html.replaceAll('href="/app"','href="https://github.com/swarn007-byte/Relay#installation"')
 .replace('Open dashboard','View on GitHub').replace('Create your account','Get started on GitHub')
 .replace('Start small.<br><em>Build something great.</em>','Good things are<br><em>open source.</em>')
 .replace('Start with a free workspace, track your deliveries, and keep your webhooks moving. You can also self-host the open-source engine.','Run Relay on your own infrastructure. The open-source engine includes signed delivery, retries, and a management dashboard.')
 .replace('10,000 events per project, per month. Retries included.','Everything you need to start delivering webhooks reliably.')
 .replace('Free <span>/ developer plan</span>','Free <span>/ self-hosted</span>')
 .replace('Paid subscriptions are not available yet. Self-hosting infrastructure costs depend on your deployment.','The hosted dashboard is not available yet. Self-hosting infrastructure costs depend on your deployment.');
if(/href="\/app"/.test(html))throw Error('Static site must not link to an unavailable dashboard.');
await mkdir(output,{recursive:true});await writeFile(resolve(output,'index.html'),html);await copyFile('website/style.css',resolve(output,'style.css'));
console.log(`Built static landing page in ${output}`);
