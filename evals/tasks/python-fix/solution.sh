set -e
sed -i 's/result\[row\["region"\]\] = float(row\["amount"\])/result[row["region"]] = result.get(row["region"], 0) + float(row["amount"])/' report.py
