def transform(s):
    return s[::-1]


def main():
    text = "hello"
    with open("out.txt", "w") as f:
        f.write(transform(text))


if __name__ == "__main__":
    main()
