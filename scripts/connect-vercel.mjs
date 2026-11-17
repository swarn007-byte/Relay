// Run after the durable backend is deployed: node scripts/connect-vercel.mjs https://YOUR-SERVICE.onrender.com
// Requires an authenticated Vercel CLI. No credentials are written into the deployment.
import {mkdtemp,writeFile,readFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
const input=process.argv[2];
if(!input)throw new Error('Supply the HTTPS backend origin.');
const backend=new URL(input);
if(backend.protocol!=='https:'||backend.username||backend.password||backend.pathname!=='/'||backend.search||backend.hash)throw new Error('Use a public HTTPS origin without a path or credentials.');
for(const [path,type] of [['/health','health'],['/app','app']]){
 const res=await fetch(new URL(path,backend),{signal:AbortSignal.timeout(20000)});
 if(!res.ok)throw new Error(`Backend ${path} returned ${res.status}`);
 if(type==='health'&&(await res.json()).status!=='ok')throw new Error('Backend is not healthy');
 if(type==='app'&&!(await res.text()).includes('id="auth-form"'))throw new Error('Backend does not serve the Relay dashboard');
}
const dir=await mkdtemp(join(tmpdir(),'relay-vercel-'));
await writeFile(join(dir,'vercel.json'),JSON.stringify({version:2,rewrites:[{source:'/:path*',destination:`${backend.origin}/:path*`}],framework:null},null,2));
await writeFile(join(dir,'index.html'),'Relay backend proxy');
const {mkdir}=await import('node:fs/promises');await mkdir(join(dir,'.vercel'));
await writeFile(join(dir,'.vercel/project.json'),await readFile(resolve('website/.vercel/project.json')));
const args=['exec','--yes','--package','vercel','--','vercel','deploy',dir,'--prod','--yes'];
if(process.env.RELAY_VERCEL_CONFIG_DIR)args.push('--global-config',process.env.RELAY_VERCEL_CONFIG_DIR);
const result=spawnSync('npm',args,{stdio:'inherit',env:process.env});
process.exit(result.status??1);
