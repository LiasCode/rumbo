"""Print the total sales of each region, one per line, sorted by region."""

import csv


def totals(path):
    result = {}

    with open(path, newline="") as handle:
        for row in csv.DictReader(handle):
            result[row["region"]] = float(row["amount"])

    return result


if __name__ == "__main__":
    for region, amount in sorted(totals("sales.csv").items()):
        print(f"{region} {amount:.2f}")
