
import { NginxPackage } from './nginx/mod.ts'

async function main() {
    // Install nginx with apt
    let opts = {
        args: ["apt-get", "install", "-y", NginxPackage]
    };
    let proc = await Deno.run(opts);
}

main();
