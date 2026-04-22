const USERS = { "u1": { id: "u1", name: "alice" }, "u2": { id: "u2", name: "bob" } };

function handleHealth(req, res) {
    res.statusCode = 200;
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify({ status: "ok" }));
}

function handleUser(req, res, id) {
    const user = USERS[id];
    if (!user) {
        res.statusCode = 404;
        res.end(JSON.stringify({ error: "not found" }));
        return;
    }
    res.statusCode = 200;
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify({ id: user.id, name: "user-"
