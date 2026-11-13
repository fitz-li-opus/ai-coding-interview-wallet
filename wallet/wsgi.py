from app import create_app

app = create_app()

if __name__ == "__main__":
    # 0.0.0.0 so the provider can reach us from Docker (host.docker.internal).
    app.run(host="0.0.0.0", port=8080)
