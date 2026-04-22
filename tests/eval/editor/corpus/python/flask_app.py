from flask import Flask, jsonify, request

app = Flask(__name__)

USERS = {}


@app.route("/users", methods=["GET"])
def list_users():
    return jsonify(list(USERS.values()))


@app.route("/users/<user_id>", methods=["GET"])
def get_user(user_id):
    user = USERS.get(user_id)
    if user is None:
        return jsonify({"error": "not found"}), 404
    return jsonify(user)


@app.route("/users", methods=["POST"])
def create_user():
    data = request.get_json(silent=True) or {}
    user_id = data.get("id")
    email = data.get("email")
    if not user_id or not email:
        return jsonify({"error": "id and email required"}), 400
    USERS[user_id] = {"id": user_id, "email": email, "name": data.get("name", "")}
    return jsonify(USERS[user_id]), 201


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=5000)
