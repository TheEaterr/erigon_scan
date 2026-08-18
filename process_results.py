import matplotlib.pyplot as plt
import json
import numpy as np

MAX_LINES = 86004957

def process_contracts_storage():
    big_contracts = []
    storage_sizes = np.zeros(86004957)
    current_line = 0
    
    with open("contracts_storage.jsonl") as f:
        for line in f:
            contract_info = json.loads(line)
            num_slots = int(contract_info["numSlots"])
            storage_sizes[current_line] = num_slots
            if num_slots >= 1e5:
                big_contracts.append(contract_info)
                
            current_line += 1
            if current_line % 1e5 == 0:
                print(f"Current progress {current_line} / 86004957")
                
    storage_sizes.dump("storage_sizes.pickle")
    with open("big_contracts.json", mode="w") as f:
        json.dump(big_contracts, f)
                
    
if __name__ == "__main__":
    process_contracts_storage()