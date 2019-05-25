
async function main() {
    // Make a user for Carlos
    let opts = {
        args: ["useradd", "carlos"]
    };
    let proc = await Deno.run(opts);
}

main();
