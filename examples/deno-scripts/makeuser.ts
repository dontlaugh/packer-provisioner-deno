
async function main() {
    let opts = {
        args: ["useradd", "carlos"]
    };
    let proc = await Deno.run(opts);
}

main();
