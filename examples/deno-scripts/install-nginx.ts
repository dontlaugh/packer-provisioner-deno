
async function main() {
    // Install nginx with apt
    let opts = {
        args: ["apt-get", "install", "-y", "nginx"]
    };
    let proc = await Deno.run(opts);
}

main();